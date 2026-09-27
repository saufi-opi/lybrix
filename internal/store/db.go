// Package store: pgx pool wrapper + the query layer over ParadeDB
// (Postgres 17 + pgvector + pg_search). All control-plane writes funnel
// through here so the state machine — which transitions are legal, when
// leases are taken, how atomic counters bump — is testable in one place.
//
// Lock-ordering rule (R-36 §1.3.5): every multi-statement tx touches the
// tables in the order chunks → shards → documents → events/metrics_rollup,
// and documents is always the LAST table touched in any tx. After the R-36
// phase split (parser claim/finish txs, embedder single write tx, janitor
// one short tx per step) every multi-statement tx conforms, so residual
// contention degrades to short blocking healed by the session timeouts +
// TxWithRetry below — never a 40P01.
//
// No-network-I/O-inside-a-tx rule (R-36): S3 download/upload, anydoc/docling
// conversion, TEI embedding HTTP calls, and Redis round-trips never run
// inside an open tx. A tx that holds row locks across network/CPU work is a
// standing lock reservoir that widens every contention window — the shape
// that produced the 2026-09-27 deadlock storm (12,400-shard stall, 196
// 40P01s, parser fleet crash-looping on `apply schema.sql: deadlock
// detected`). See docs/BACKLOG.md R-36.
package store

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/saufi-opi/lybrix/internal/config"
)

//go:embed schema.sql
var schemaSQL string

// DB wraps a pgxpool with transaction helpers.
type DB struct {
	Pool *pgxpool.Pool
}

// Timeouts are the pool-wide session timeouts (R-36 §1.3 move 3). Zero
// fields fall back to the built-in defaults.
type Timeouts struct {
	Statement time.Duration // statement_timeout — per-statement wall clock
	Lock      time.Duration // lock_timeout — per-lock-acquisition wait
	IdleTx    time.Duration // idle_in_transaction_session_timeout
}

// builtinTimeouts apply when the caller passes zero Timeouts (tests, or
// callers wired before config carried the settings).
var builtinTimeouts = Timeouts{
	Statement: 60 * time.Second,
	Lock:      5 * time.Second,
	IdleTx:    120 * time.Second,
}

// withDefaults fills zero fields from the built-ins.
func (t Timeouts) withDefaults() Timeouts {
	if t.Statement == 0 {
		t.Statement = builtinTimeouts.Statement
	}
	if t.Lock == 0 {
		t.Lock = builtinTimeouts.Lock
	}
	if t.IdleTx == 0 {
		t.IdleTx = builtinTimeouts.IdleTx
	}
	return t
}

// TimeoutsFrom projects the config settings onto store.Timeouts — the one
// projection point so cmd wiring never hand-builds the struct.
func TimeoutsFrom(settings *config.Settings) Timeouts {
	return Timeouts{
		Statement: settings.PGStatementTimeout,
		Lock:      settings.PGLockTimeout,
		IdleTx:    settings.PGIdleTxTimeout,
	}
}

// NewPool opens a pool and bootstraps the schema (advisory-lock guarded so
// concurrent replicas converge instead of racing the DDL). Every pooled
// connection carries the session timeouts (R-36): a wedged statement, an
// un-won lock wait, or an idle-in-tx session dies on its own instead of
// becoming contention reservoir for every other writer.
func NewPool(ctx context.Context, dsn string, timeouts Timeouts) (*DB, error) {
	to := timeouts.withDefaults()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	cfg.MaxConns = 20
	cfg.MaxConnLifetime = time.Hour
	// Skip zero-valued settings: a configured 0 means "unset" is the intent
	// for that knob (e.g. callers that want the DB server's own defaults).
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		if to.Statement > 0 {
			if _, err := conn.Exec(ctx, fmt.Sprintf("SET statement_timeout = %d", to.Statement.Milliseconds())); err != nil {
				return err
			}
		}
		if to.Lock > 0 {
			if _, err := conn.Exec(ctx, fmt.Sprintf("SET lock_timeout = %d", to.Lock.Milliseconds())); err != nil {
				return err
			}
		}
		if to.IdleTx > 0 {
			if _, err := conn.Exec(ctx, fmt.Sprintf("SET idle_in_transaction_session_timeout = %d", to.IdleTx.Milliseconds())); err != nil {
				return err
			}
		}
		return nil
	}
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

// bootstrapAttempts bounds the BootstrapSchema retry ladder.
const bootstrapAttempts = 5

// ddlLockTimeoutCap caps the bootstrap connection's lock_timeout: lock
// ACQUISITION (not the DDL's own execution) is the contention guard, and a
// fleet of workers booting against a loaded DB should give up quickly and
// retry rather than queue for the full pool-wide lock timeout.
const ddlLockTimeoutCap = 2 * time.Second

// BootstrapSchema applies deploy/schema.sql idempotently under a
// pg_advisory_lock. The file is embedded in the binary (go:embed above).
//
// Deadlock-proofing (R-36): schema.sql's DDL (CREATE INDEX …, ALTER TABLE …,
// the bm25 DO block) queues SHARE/ACCESS EXCLUSIVE locks BEFORE the
// IF [NOT] EXISTS check short-circuits, so a boot against an already-
// migrated DB still waits on every open writer — and a live writer's next
// statement queues behind the DDL, closing the 40P01 cycle inside schema.sql
// (the parser-fleet crash loop). Two guards make a boot unable to deadlock:
//
//   - statement_timeout = 0 on the DDL connection: a multi-minute HNSW
//     build is never killed mid-flight (killing it would restart the build
//     forever, recreating the crash-loop shape). The build itself is not
//     the contention hazard; lock acquisition is.
//   - lock_timeout = min(PGLockTimeout, 2s) + bounded jittered retry (DDL
//     is idempotent, so a retry re-runs the whole file safely): on 40P01/
//     55P03 the connection releases everything, backs off, and re-runs.
//
// The advisory lock is still the fleet-wide serializer: only one boot applies
// the file at a time, so concurrent boots queue HERE (bounded by the retry
// ladder) rather than deadlocking inside the DDL.
func (d *DB) BootstrapSchema(ctx context.Context) error {
	var lastErr error
	for attempt := 0; attempt < bootstrapAttempts; attempt++ {
		if attempt > 0 {
			testRetrySleep(retryBackoff(attempt))
		}
		err := d.bootstrapSchemaOnce(ctx)
		if err == nil {
			return nil
		}
		if !IsTransientPGError(err) {
			return err
		}
		lastErr = err
		slog.Warn("schema bootstrap hit transient PG error — retrying",
			"attempt", attempt+1, "of", bootstrapAttempts, "err", err)
	}
	return fmt.Errorf("apply schema.sql after %d attempts: %w", bootstrapAttempts, lastErr)
}

func (d *DB) bootstrapSchemaOnce(ctx context.Context) error {
	conn, err := d.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer func() {
		// RESET the DDL overrides before the connection returns to the
		// pool: a released connection keeps its session state for up to
		// MaxConnLifetime, and ordinary statements on recycled connections
		// would otherwise lose the pool-wide statement_timeout floor.
		_, _ = conn.Exec(context.WithoutCancel(ctx), "RESET statement_timeout; RESET lock_timeout")
		conn.Release()
	}()
	if _, err := conn.Exec(ctx, "SET statement_timeout = 0"); err != nil {
		return fmt.Errorf("ddl statement_timeout: %w", err)
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf("SET lock_timeout = %d", ddlLockTimeout().Milliseconds())); err != nil {
		return fmt.Errorf("ddl lock_timeout: %w", err)
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock(918273645)"); err != nil {
		return fmt.Errorf("schema advisory lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock(918273645)")
	}()
	if _, err := conn.Exec(ctx, schemaSQL); err != nil {
		return fmt.Errorf("apply schema.sql: %w", err)
	}
	slog.Info("schema bootstrap complete")
	return nil
}

// ddlLockTimeout is the session-level lock_timeout for DDL connections —
// min(pool floor, 2s cap), the guard against queueing behind live writers.
func ddlLockTimeout() time.Duration {
	cap := ddlLockTimeoutCap
	if builtinTimeouts.Lock < cap {
		cap = builtinTimeouts.Lock
	}
	return cap
}

// ensureDimIndexAttempts bounds the EnsureDimIndex retry ladder.
const ensureDimIndexAttempts = 3

// EnsureDimIndex creates the per-dimension partial HNSW index under the
// same guards as BootstrapSchema (R-36): statement_timeout = 0 (the build
// runs unbounded), lock_timeout = min(floor, 2s), bounded retry — the same
// boot-time 40P01 class fires against CREATE INDEX here (fresh-dim provision
// while writers hold chunk locks). Retry is safe: index creation either
// completed or left nothing behind (the advisory lock serializes attempts,
// and a failed CREATE INDEX leaves no partial index).
func (d *DB) EnsureDimIndex(ctx context.Context, dim int) error {
	if dim < 1 || dim > 2000 {
		return fmt.Errorf("vector_dim %d out of range 1..2000", dim)
	}
	name := fmt.Sprintf("ix_chunks_hnsw_%d", dim)
	var lastErr error
	for attempt := 0; attempt < ensureDimIndexAttempts; attempt++ {
		if attempt > 0 {
			testRetrySleep(retryBackoff(attempt))
		}
		err := d.ensureDimIndexOnce(ctx, dim, name)
		if err == nil {
			return nil
		}
		if !IsTransientPGError(err) {
			return err
		}
		lastErr = err
		slog.Warn("dim index create hit transient PG error — retrying",
			"index", name, "attempt", attempt+1, "of", ensureDimIndexAttempts, "err", err)
	}
	return fmt.Errorf("ensure %s after %d attempts: %w", name, ensureDimIndexAttempts, lastErr)
}

func (d *DB) ensureDimIndexOnce(ctx context.Context, dim int, name string) error {
	conn, err := d.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer func() {
		// Same RESET-before-release hygiene as bootstrapSchemaOnce.
		_, _ = conn.Exec(context.WithoutCancel(ctx), "RESET statement_timeout; RESET lock_timeout")
		conn.Release()
	}()
	if _, err := conn.Exec(ctx, "SET statement_timeout = 0"); err != nil {
		return fmt.Errorf("ddl statement_timeout: %w", err)
	}
	if _, err := conn.Exec(ctx, fmt.Sprintf("SET lock_timeout = %d", ddlLockTimeout().Milliseconds())); err != nil {
		return fmt.Errorf("ddl lock_timeout: %w", err)
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock(918273645)"); err != nil {
		return fmt.Errorf("dim index advisory lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock(918273645)")
	}()
	var exists bool
	if err := conn.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = $1)`, name).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	// CREATE INDEX IF NOT EXISTS would race benignly anyway, but the probe
	// keeps the common path silent; plain CREATE INDEX under the lock.
	stmt := fmt.Sprintf(`CREATE INDEX %s ON chunks
		USING hnsw ((embedding::vector(%d)) vector_cosine_ops)
		WHERE is_parent = FALSE AND vector_dims(embedding) = %d`, name, dim, dim)
	if _, err := conn.Exec(ctx, stmt); err != nil {
		return fmt.Errorf("ensure %s: %w", name, err)
	}
	slog.Info("provisioned dimension index", "index", name)
	return nil
}

// TxWithRetry runs fn inside one short transaction, re-running the WHOLE fn
// on the transient contention classes (40P01/40001/55P03 — see pgerr.go).
// attempts < 1 means 1. Non-transient errors return immediately; exhausted
// attempts return the last error wrapped. fn must be idempotent: a retry
// re-executes it from scratch (claim/done/resplit/embed-write all are —
// ClaimShard's state guard, delete+rebuild embed, idempotent split probe).
//
// This is the self-healing floor for every short write tx (R-36 §1.3 move
// 3): a contention blip costs milliseconds of backoff, not a whole parse.
var testRetrySleep = time.Sleep

// retryBackoff is base*2^n with ±25% jitter (base 50ms, cap 2s).
func retryBackoff(attempt int) time.Duration {
	base := 50 * time.Millisecond
	max := 2 * time.Second
	for i := 1; i < attempt; i++ {
		base *= 2
		if base > max {
			base = max
			break
		}
	}
	jitter := base * time.Duration(rand.Intn(51)-25) / 100 // ±25%
	d := base + jitter
	if d < 0 {
		d = 0
	}
	if d > max*2 {
		d = max * 2
	}
	return d
}

func (d *DB) TxWithRetry(ctx context.Context, attempts int, fn func(pgx.Tx) error) error {
	// The retry loop itself is the pure free function below; this method
	// only binds the real Tx (a test cannot override a promoted method's
	// embedded receiver, so the loop is unit-tested via txWithRetryLoop).
	return txWithRetryLoop(ctx, attempts, fn, d.Tx)
}

func txWithRetryLoop(ctx context.Context, attempts int, fn func(pgx.Tx) error,
	txFn func(context.Context, func(pgx.Tx) error) error) error {
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		err := txFn(ctx, fn)
		if err == nil {
			return nil
		}
		if !IsTransientPGError(err) {
			return err
		}
		lastErr = err
		if attempt < attempts-1 {
			testRetrySleep(retryBackoff(attempt + 1))
		}
	}
	return lastErr
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
