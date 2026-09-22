package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
)

// ErrModelNotFound / ErrModelInUse / ErrModelDefault are the typed registry
// failures — the API maps them to 404 / 409 / 409.
var (
	ErrModelNotFound = errors.New("embedding model not found")
	ErrModelInUse    = errors.New("model bound to collection(s)")
	ErrModelDefault  = errors.New("cannot delete the default model")
)

// EmbeddingSeed is the first-boot seed config — the retired EMBED_* env
// vars are read ONLY here, when the registry table is empty (hard cut).
type EmbeddingSeed struct {
	Provider  string
	ModelID   string
	Dim       int
	IngestURL string
	QueryURL  string
}

// modelCols is the SELECT list for embedding_models rows. api_key is never
// selected raw — has_api_key is computed from IS NOT NULL.
const modelCols = `id, name, provider, model_id, ingest_url, query_url,
	(api_key IS NOT NULL) AS has_api_key, vector_dim, query_prefix,
	batch_size, ctx_budget, truncate_chars, is_default, created_at, updated_at`

func scanModel(row pgx.Row) (*EmbeddingModel, error) {
	var m EmbeddingModel
	err := row.Scan(&m.ID, &m.Name, &m.Provider, &m.ModelID, &m.IngestURL,
		&m.QueryURL, &m.HasAPIKey, &m.VectorDim, &m.QueryPrefix, &m.BatchSize,
		&m.CtxBudget, &m.TruncateChars, &m.IsDefault, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// ListEmbeddingModels returns every registered model, default first.
func (d *DB) ListEmbeddingModels(ctx context.Context) ([]*EmbeddingModel, error) {
	rows, err := d.Pool.Query(ctx, `SELECT `+modelCols+`
		FROM embedding_models ORDER BY is_default DESC, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*EmbeddingModel
	for rows.Next() {
		m, err := scanModel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetEmbeddingModel fetches one registered model by id; nil when absent.
func (d *DB) GetEmbeddingModel(ctx context.Context, id string) (*EmbeddingModel, error) {
	row := d.Pool.QueryRow(ctx, `SELECT `+modelCols+` FROM embedding_models WHERE id = $1`, id)
	m, err := scanModel(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return m, err
}

// GetDefaultEmbeddingModel returns the is_default row; nil when the
// registry is empty.
func (d *DB) GetDefaultEmbeddingModel(ctx context.Context) (*EmbeddingModel, error) {
	row := d.Pool.QueryRow(ctx, `SELECT `+modelCols+`
		FROM embedding_models WHERE is_default ORDER BY created_at LIMIT 1`)
	m, err := scanModel(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return m, err
}

// InsertEmbeddingModel registers a model. The previous default (if any) is
// cleared first when the new row is_default; EnsureDimIndex provisions the
// dimension's HNSW up front.
func (d *DB) InsertEmbeddingModel(ctx context.Context, m *EmbeddingModel, apiKey *string) (*EmbeddingModel, error) {
	if m.IsDefault {
		if _, err := d.Pool.Exec(ctx, `UPDATE embedding_models SET is_default = FALSE WHERE is_default`); err != nil {
			return nil, err
		}
	}
	row := d.Pool.QueryRow(ctx, `INSERT INTO embedding_models
		(name, provider, model_id, ingest_url, query_url, api_key, vector_dim,
		 query_prefix, batch_size, ctx_budget, truncate_chars, is_default)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		RETURNING `+modelCols,
		m.Name, m.Provider, m.ModelID, m.IngestURL, m.QueryURL, apiKey,
		m.VectorDim, m.QueryPrefix, m.BatchSize, m.CtxBudget, m.TruncateChars, m.IsDefault)
	out, err := scanModel(row)
	if err != nil {
		return nil, err
	}
	if err := d.EnsureDimIndex(ctx, out.VectorDim); err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateEmbeddingModel partially updates one model. apiKey == nil means
// unchanged; dim change provisions the new dimension's HNSW.
func (d *DB) UpdateEmbeddingModel(ctx context.Context, id string, m *EmbeddingModel, apiKey *string) (*EmbeddingModel, error) {
	if m.IsDefault {
		if _, err := d.Pool.Exec(ctx, `UPDATE embedding_models SET is_default = FALSE
			WHERE is_default AND id <> $1`, id); err != nil {
			return nil, err
		}
	}
	row := d.Pool.QueryRow(ctx, `UPDATE embedding_models SET
		name = $2, provider = $3, model_id = $4, ingest_url = $5, query_url = $6,
		api_key = COALESCE($7, api_key), vector_dim = $8, query_prefix = $9,
		batch_size = $10, ctx_budget = $11, truncate_chars = $12,
		is_default = $13, updated_at = NOW()
		WHERE id = $1
		RETURNING `+modelCols,
		id, m.Name, m.Provider, m.ModelID, m.IngestURL, m.QueryURL, apiKey,
		m.VectorDim, m.QueryPrefix, m.BatchSize, m.CtxBudget, m.TruncateChars, m.IsDefault)
	out, err := scanModel(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := d.EnsureDimIndex(ctx, out.VectorDim); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteEmbeddingModel removes a model row. It refuses (ErrModelInUse /
// ErrModelDefault) when collections still reference it or it is the
// default — the caller maps those to 409.
func (d *DB) DeleteEmbeddingModel(ctx context.Context, id string) error {
	m, err := d.GetEmbeddingModel(ctx, id)
	if err != nil {
		return err
	}
	if m == nil {
		return ErrModelNotFound
	}
	if m.IsDefault {
		return ErrModelDefault
	}
	n, err := d.CountCollectionRefs(ctx, id)
	if err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%w: %d", ErrModelInUse, n)
	}
	_, err = d.Pool.Exec(ctx, `DELETE FROM embedding_models WHERE id = $1`, id)
	return err
}

// BindCollectionModel rebinds an existing collection to another registered
// model (metadata sync only — no vector rewrite happens in v1; stale-dim
// vectors are excluded from dense search by the vector_dims filter). The
// legacy columns stay in sync from the bound row.
func (d *DB) BindCollectionModel(ctx context.Context, collectionID, modelID string) (*Collection, error) {
	m, err := d.GetEmbeddingModel(ctx, modelID)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, ErrModelNotFound
	}
	row := d.Pool.QueryRow(ctx, `UPDATE collections SET
		embedding_model_id = $2,
		embedding_model = $3, vector_dim = $4
		WHERE id = $1
		RETURNING id, name, embedding_model, vector_dim, created_at`,
		collectionID, m.ID, m.ModelID, m.VectorDim)
	var c Collection
	serr := row.Scan(&c.ID, &c.Name, &c.EmbeddingModel, &c.VectorDim, &c.CreatedAt)
	if serr == pgx.ErrNoRows {
		return nil, nil
	}
	if serr != nil {
		return nil, serr
	}
	if err := d.EnsureDimIndex(ctx, m.VectorDim); err != nil {
		return nil, err
	}
	return &c, nil
}

// CountCollectionRefs counts collections bound to a model.
func (d *DB) CountCollectionRefs(ctx context.Context, modelID string) (int, error) {
	var n int
	err := d.Pool.QueryRow(ctx,
		`SELECT count(*) FROM collections WHERE embedding_model_id = $1`, modelID).Scan(&n)
	return n, err
}

// SeedDefaultEmbeddingModel seeds exactly one default row from the retired
// EMBED_* env vars when the registry is empty (idempotent INSERT … WHERE
// NOT EXISTS), then provisions the seed dimension's HNSW index.
func (d *DB) SeedDefaultEmbeddingModel(ctx context.Context, seed EmbeddingSeed) error {
	if _, err := d.Pool.Exec(ctx, `INSERT INTO embedding_models
		(name, provider, model_id, ingest_url, query_url, vector_dim,
		 query_prefix, batch_size, ctx_budget, truncate_chars, is_default)
		SELECT 'bge-m3 @ seed', $1, $2, $3, $4, $5, 'search_query: ', 48, 1900, 6000, TRUE
		WHERE NOT EXISTS (SELECT 1 FROM embedding_models)`,
		seed.Provider, seed.ModelID, seed.IngestURL, seed.QueryURL, seed.Dim); err != nil {
		return err
	}
	// The seed row's dim needs its index regardless of whether the INSERT
	// fired this boot or a previous one (criterion 3: index present).
	if err := d.EnsureDimIndex(ctx, seed.Dim); err != nil {
		return err
	}
	return nil
}

// EnsureDimIndex creates the per-dimension partial HNSW index
// ix_chunks_hnsw_<dim> when absent, under the same advisory lock the schema
// bootstrap uses. The dense CTE matches the expression + predicate or the
// planner won't use the index.
func (d *DB) EnsureDimIndex(ctx context.Context, dim int) error {
	if dim < 1 || dim > 2000 {
		return fmt.Errorf("vector_dim %d out of range 1..2000", dim)
	}
	name := fmt.Sprintf("ix_chunks_hnsw_%d", dim)
	conn, err := d.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
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

// ResolveCollectionModel is the single resolution rule (PLAN.md §5): the
// doc's/collection's bound model; legacy NULL binding → the default row;
// empty registry → nil (caller raises EMBED_DIM_MISMATCH).
func (d *DB) ResolveCollectionModel(ctx context.Context, collectionID string) (*EmbeddingModel, error) {
	row := d.Pool.QueryRow(ctx, `SELECT `+modelCols+`
		FROM embedding_models m
		WHERE m.id = COALESCE(
			(SELECT embedding_model_id FROM collections WHERE id = $1),
			(SELECT id FROM embedding_models WHERE is_default ORDER BY created_at LIMIT 1))
		LIMIT 1`, collectionID)
	m, err := scanModel(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return m, err
}
