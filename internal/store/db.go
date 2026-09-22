// Package store: pgx pool wrapper + the query layer over ParadeDB
// (Postgres 17 + pgvector + pg_search). All control-plane writes funnel
// through here so the state machine — which transitions are legal, when
// leases are taken, how atomic counters bump — is testable in one place.
package store

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed schema.sql
var schemaSQL string

// DB wraps a pgxpool with transaction helpers.
type DB struct {
	Pool *pgxpool.Pool
}

// NewPool opens a pool and bootstraps the schema (advisory-lock guarded so
// concurrent replicas converge instead of racing the DDL).
func NewPool(ctx context.Context, dsn string) (*DB, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	cfg.MaxConns = 20
	cfg.MaxConnLifetime = time.Hour
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	db := &DB{Pool: pool}
	if err := db.BootstrapSchema(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return db, nil
}

// BootstrapSchema applies deploy/schema.sql idempotently under a
// pg_advisory_lock. The file is embedded in the binary (go:embed above).
func (d *DB) BootstrapSchema(ctx context.Context) error {
	conn, err := d.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock(918273645)"); err != nil {
		return fmt.Errorf("schema advisory lock: %w", err)
	}
	defer conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock(918273645)")
	if _, err := conn.Exec(ctx, schemaSQL); err != nil {
		return fmt.Errorf("apply schema.sql: %w", err)
	}
	slog.Info("schema bootstrap complete")
	return nil
}

// Close releases the pool.
func (d *DB) Close() { d.Pool.Close() }

// Ping proves the DB is reachable (health probe).
func (d *DB) Ping(ctx context.Context) error { return d.Pool.Ping(ctx) }

// Tx runs fn inside one transaction; commit on success, rollback on error.
// This mirrors 1.0's session_scope: every handler runs in one tx and the
// runner ACKs only after commit.
func (d *DB) Tx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			return fmt.Errorf("tx failed (%v); rollback also failed (%v)", err, rbErr)
		}
		return err
	}
	return tx.Commit(ctx)
}
