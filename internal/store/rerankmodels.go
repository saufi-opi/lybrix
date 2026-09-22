package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrRerankModelNotFound / ErrRerankModelInUse mirror the embedding
// registry's typed failures — the API maps them to 404 / 409.
var (
	ErrRerankModelNotFound = errors.New("rerank model not found")
	ErrRerankModelInUse    = errors.New("rerank model bound to collection(s)")
)

// RerankSeed is the first-boot seed config — RERANK_ENABLED / RERANK_MODEL /
// TEI_RERANK_URL are read ONLY here, when the rerank_models table is empty
// and seeding is enabled (seed-only env treatment, mirroring EmbedSeed).
type RerankSeed struct {
	Enabled  bool
	ModelID  string
	QueryURL string
}

// RerankModel mirrors one `rerank_models` row. api_key is deliberately NOT
// a field — write-only, never serialized out; HasAPIKey carries presence.
type RerankModel struct {
	ID            string
	Name          string
	Provider      string // tei | openai (openai = /v1/rerank-compatible)
	ModelID       string
	QueryURL      string
	HasAPIKey     bool
	TruncateChars int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// rerankModelCols is the SELECT list for rerank_models rows; api_key is
// never selected raw — has_api_key is computed from IS NOT NULL.
const rerankModelCols = `id, name, provider, model_id, query_url,
	(api_key IS NOT NULL) AS has_api_key, truncate_chars, created_at, updated_at`

func scanRerankModel(row pgx.Row) (*RerankModel, error) {
	var m RerankModel
	err := row.Scan(&m.ID, &m.Name, &m.Provider, &m.ModelID, &m.QueryURL,
		&m.HasAPIKey, &m.TruncateChars, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// ListRerankModels returns every registered reranker in registration order.
func (d *DB) ListRerankModels(ctx context.Context) ([]*RerankModel, error) {
	rows, err := d.Pool.Query(ctx, `SELECT `+rerankModelCols+`
		FROM rerank_models ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*RerankModel
	for rows.Next() {
		m, err := scanRerankModel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetRerankModel fetches one registered reranker by id; nil when absent.
func (d *DB) GetRerankModel(ctx context.Context, id string) (*RerankModel, error) {
	row := d.Pool.QueryRow(ctx, `SELECT `+rerankModelCols+` FROM rerank_models WHERE id = $1`, id)
	m, err := scanRerankModel(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return m, err
}

// InsertRerankModel registers a reranker.
func (d *DB) InsertRerankModel(ctx context.Context, m *RerankModel, apiKey *string) (*RerankModel, error) {
	row := d.Pool.QueryRow(ctx, `INSERT INTO rerank_models
		(name, provider, model_id, query_url, api_key, truncate_chars)
		VALUES ($1,$2,$3,$4,$5,$6)
		RETURNING `+rerankModelCols,
		m.Name, m.Provider, m.ModelID, m.QueryURL, apiKey, m.TruncateChars)
	return scanRerankModel(row)
}

// UpdateRerankModel partially updates one reranker. apiKey == nil means
// unchanged.
func (d *DB) UpdateRerankModel(ctx context.Context, id string, m *RerankModel, apiKey *string) (*RerankModel, error) {
	row := d.Pool.QueryRow(ctx, `UPDATE rerank_models SET
		name = $2, provider = $3, model_id = $4, query_url = $5,
		api_key = COALESCE($6, api_key), truncate_chars = $7, updated_at = NOW()
		WHERE id = $1
		RETURNING `+rerankModelCols,
		id, m.Name, m.Provider, m.ModelID, m.QueryURL, apiKey, m.TruncateChars)
	out, err := scanRerankModel(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return out, err
}

// DeleteRerankModel removes a reranker row. It refuses (ErrRerankModelInUse)
// when collections still reference it — the caller maps that to 409.
func (d *DB) DeleteRerankModel(ctx context.Context, id string) error {
	m, err := d.GetRerankModel(ctx, id)
	if err != nil {
		return err
	}
	if m == nil {
		return ErrRerankModelNotFound
	}
	n, err := d.CountCollectionRerankRefs(ctx, id)
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: %d", ErrRerankModelInUse, n)
	}
	_, err = d.Pool.Exec(ctx, `DELETE FROM rerank_models WHERE id = $1`, id)
	return err
}

// BindCollectionReranker sets or clears a collection's reranker binding
// (modelID == "" clears). Returns the collection or nil when unknown.
func (d *DB) BindCollectionReranker(ctx context.Context, collectionID, modelID string) (*Collection, error) {
	if modelID != "" {
		m, err := d.GetRerankModel(ctx, modelID)
		if err != nil {
			return nil, err
		}
		if m == nil {
			return nil, ErrRerankModelNotFound
		}
	}
	row := d.Pool.QueryRow(ctx, `UPDATE collections SET rerank_model_id = $2
		WHERE id = $1
		RETURNING `+collectionCols, collectionID, nullableID(modelID))
	c, err := scanCollection(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return c, err
}

// CountCollectionRerankRefs counts collections bound to a reranker.
func (d *DB) CountCollectionRerankRefs(ctx context.Context, modelID string) (int, error) {
	var n int
	err := d.Pool.QueryRow(ctx,
		`SELECT count(*) FROM collections WHERE rerank_model_id = $1`, modelID).Scan(&n)
	return n, err
}

// nullableID maps "" to a SQL NULL for optional UUID bindings.
func nullableID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

// SeedDefaultRerankModel seeds exactly one reranker row from the seed-only
// RERANK_* env vars when the table is empty (idempotent INSERT … WHERE NOT
// EXISTS). Seeding runs only when RERANK_ENABLED=true — an unset reranker
// leaves the table empty and no search ever reranks.
func (d *DB) SeedDefaultRerankModel(ctx context.Context, seed RerankSeed) error {
	if !seed.Enabled {
		return nil
	}
	_, err := d.Pool.Exec(ctx, `INSERT INTO rerank_models
		(name, provider, model_id, query_url, truncate_chars)
		SELECT 'tei-rerank', 'tei', $1, $2, 6000
		WHERE NOT EXISTS (SELECT 1 FROM rerank_models)`,
		seed.ModelID, seed.QueryURL)
	return err
}

// ResolveCollectionReranker resolves the collection's bound reranker row,
// or nil when unbound/unknown (single-collection search's default source).
func (d *DB) ResolveCollectionReranker(ctx context.Context, collectionID string) (*RerankModel, error) {
	row := d.Pool.QueryRow(ctx, `SELECT `+rerankModelCols+`
		FROM rerank_models m
		JOIN collections c ON c.rerank_model_id = m.id
		WHERE c.id = $1
		LIMIT 1`, collectionID)
	m, err := scanRerankModel(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return m, err
}

// GetRerankModelWithKey fetches one reranker row plus its write-only key.
// The secret crosses ONLY this boundary — callers use it to build a
// pipeline.RerankClient and must never serialize it into a response, log,
// or event. The registry struct stays key-free; this return shape exists so
// runtime rerank and the saved-model probe can authenticate against
// openai-compatible endpoints.
func (d *DB) GetRerankModelWithKey(ctx context.Context, id string) (*RerankModel, string, error) {
	row := d.Pool.QueryRow(ctx, `SELECT `+rerankModelCols+`, api_key
		FROM rerank_models WHERE id = $1`, id)
	var m RerankModel
	var apiKey *string
	err := row.Scan(&m.ID, &m.Name, &m.Provider, &m.ModelID, &m.QueryURL,
		&m.HasAPIKey, &m.TruncateChars, &m.CreatedAt, &m.UpdatedAt, &apiKey)
	if err == pgx.ErrNoRows {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	key := ""
	if apiKey != nil {
		key = *apiKey
	}
	return &m, key, nil
}
