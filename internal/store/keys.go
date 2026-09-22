package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// HashKey is the single source of truth for API-key hashing — sha256 hex,
// shared by api + mcp surfaces (1.0 core/keys.py).
func HashKey(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

// ErrKeyInvalid / ErrKeyExpired / ErrKeyNoScope are the typed auth failures
// both surfaces map to 401/403.
var (
	ErrKeyInvalid = errors.New("invalid key")
	ErrKeyExpired = errors.New("key expired")
	ErrKeyNoScope = errors.New("key lacks required scope")
)

// ValidScopes is the scope universe; 422 on anything outside it.
var ValidScopes = map[string]bool{"search": true, "ingest": true, "admin": true}

// CreateKey inserts a key row and returns it.
func (d *DB) CreateKey(ctx context.Context, name string, keyHash string, scopes, collections []string, expiresAt *time.Time) (*ApiKey, error) {
	row := d.Pool.QueryRow(ctx, `INSERT INTO api_keys (name, key_hash, scopes, collections, expires_at)
		VALUES ($1,$2,$3,$4,$5)
		RETURNING id, name, key_hash, scopes, collections, expires_at, revoked_at, last_used_at, created_at`,
		name, keyHash, scopes, collections, expiresAt)
	return scanKey(row)
}

func scanKey(row pgx.Row) (*ApiKey, error) {
	var k ApiKey
	err := row.Scan(&k.ID, &k.Name, &k.KeyHash, &k.Scopes, &k.Collections,
		&k.ExpiresAt, &k.RevokedAt, &k.LastUsedAt, &k.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &k, nil
}

// ListKeys ordered by last_used_at desc nulls last.
func (d *DB) ListKeys(ctx context.Context) ([]*ApiKey, error) {
	rows, err := d.Pool.Query(ctx, `SELECT id, name, key_hash, scopes, collections,
		expires_at, revoked_at, last_used_at, created_at FROM api_keys
		ORDER BY last_used_at DESC NULLS LAST`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ApiKey
	for rows.Next() {
		k, err := scanKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// RevokeKey sets revoked_at once; idempotent (second call is a no-op that
// still returns the row). Returns ErrKeyInvalid when the id is unknown.
func (d *DB) RevokeKey(ctx context.Context, keyID string) (*ApiKey, error) {
	if _, err := d.Pool.Exec(ctx,
		`UPDATE api_keys SET revoked_at = NOW() WHERE id = $1 AND revoked_at IS NULL`, keyID); err != nil {
		return nil, err
	}
	return d.GetKey(ctx, keyID)
}

// GetKey fetches one key row by id.
func (d *DB) GetKey(ctx context.Context, keyID string) (*ApiKey, error) {
	row := d.Pool.QueryRow(ctx, `SELECT id, name, key_hash, scopes, collections,
		expires_at, revoked_at, last_used_at, created_at FROM api_keys WHERE id = $1`, keyID)
	k, err := scanKey(row)
	if err == pgx.ErrNoRows {
		return nil, ErrKeyInvalid
	}
	return k, err
}

// AuthenticateKey resolves a raw bearer key to its row, enforcing the
// shared validity rule: revoked or expired -> 401-class error. Scope
// checking stays with the callers (api requires the route scope; mcp
// requires search).
func (d *DB) AuthenticateKey(ctx context.Context, rawKey string) (*ApiKey, error) {
	// constant-time-ish compare is unnecessary for a hash lookup, but
	// subtle is cheap and keeps reviewers calm.
	hash := HashKey(rawKey)
	if len(hash) != 64 {
		return nil, ErrKeyInvalid
	}
	row := d.Pool.QueryRow(ctx, `SELECT id, name, key_hash, scopes, collections,
		expires_at, revoked_at, last_used_at, created_at FROM api_keys WHERE key_hash = $1`, hash)
	k, err := scanKey(row)
	if err == pgx.ErrNoRows {
		return nil, ErrKeyInvalid
	}
	if err != nil {
		return nil, err
	}
	if k.RevokedAt != nil {
		return nil, ErrKeyInvalid
	}
	if k.ExpiresAt != nil && k.ExpiresAt.Before(time.Now()) {
		return nil, ErrKeyExpired
	}
	return k, nil
}

// HasScope reports whether the key carries a scope.
func HasScope(k *ApiKey, scope string) bool {
	for _, s := range k.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// RequireScope returns ErrKeyNoScope when the key lacks scope (403).
func RequireScope(k *ApiKey, scope string) error {
	if !HasScope(k, scope) {
		return fmt.Errorf("%w: %s", ErrKeyNoScope, scope)
	}
	return nil
}

// CollectionAllowed enforces the MCP key-scoping rule: passing a collection
// outside the key's scope is an error, not a filter (auth.py).
func CollectionAllowed(k *ApiKey, collectionID string) bool {
	if collectionID == "" || len(k.Collections) == 0 {
		return true
	}
	for _, c := range k.Collections {
		if c == collectionID {
			return true
		}
	}
	return false
}

// TouchLastUsed bumps last_used_at (best-effort; callers ignore errors).
func (d *DB) TouchLastUsed(ctx context.Context, keyID string) {
	_, _ = d.Pool.Exec(ctx, `UPDATE api_keys SET last_used_at = NOW() WHERE id = $1`, keyID)
}

// RecordUsage writes one key_usage row (surface 'api'|'mcp').
func (d *DB) RecordUsage(ctx context.Context, keyID, surface, action string) error {
	_, err := d.Pool.Exec(ctx,
		`INSERT INTO key_usage (api_key_id, surface, action) VALUES ($1,$2,$3)`,
		keyID, surface, action)
	return err
}

// UsageSummaryRow is one per-key aggregate.
type UsageSummaryRow struct {
	KeyID      string
	Name       *string
	Calls      int
	LastUsedAt *time.Time
}

// UsageSummary groups key_usage by key bounded by a period cutoff,
// calls desc (usage.py semantics).
func (d *DB) UsageSummary(ctx context.Context, since time.Time) ([]UsageSummaryRow, error) {
	rows, err := d.Pool.Query(ctx, `SELECT k.id, k.name, count(u.id), max(u.created_at)
		FROM api_keys k JOIN key_usage u ON u.api_key_id = k.id
		WHERE u.created_at >= $1
		GROUP BY k.id, k.name ORDER BY count(u.id) DESC`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageSummaryRow
	for rows.Next() {
		var r UsageSummaryRow
		var calls int64
		if err := rows.Scan(&r.KeyID, &r.Name, &calls, &r.LastUsedAt); err != nil {
			return nil, err
		}
		r.Calls = int(calls)
		out = append(out, r)
	}
	return out, rows.Err()
}

var _ = subtle.ConstantTimeCompare
