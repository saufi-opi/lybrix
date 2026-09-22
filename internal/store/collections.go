package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// collectionCols covers the OpenAPI CollectionOut shape: the legacy
// embedding_model / vector_dim columns (kept in sync FROM the bound
// registry row), the embedding binding id, and the reranker binding id.
const collectionCols = `id, name, embedding_model, vector_dim, embedding_model_id, rerank_model_id, created_at`

func scanCollection(row pgx.Row) (*Collection, error) {
	var c Collection
	if err := row.Scan(&c.ID, &c.Name, &c.EmbeddingModel, &c.VectorDim,
		&c.EmbeddingModelID, &c.RerankModelID, &c.CreatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

// ListCollections returns all collections.
func (d *DB) ListCollections(ctx context.Context) ([]*Collection, error) {
	rows, err := d.Pool.Query(ctx,
		`SELECT `+collectionCols+` FROM collections ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Collection
	for rows.Next() {
		c, err := scanCollection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetCollection fetches one collection by id; nil when absent.
func (d *DB) GetCollection(ctx context.Context, id string) (*Collection, error) {
	row := d.Pool.QueryRow(ctx,
		`SELECT `+collectionCols+` FROM collections WHERE id = $1`, id)
	c, err := scanCollection(row)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	return c, err
}

// InsertCollection creates a collection bound to a registered model
// (MANDATORY at creation since the registry). The legacy embedding_model /
// vector_dim columns inherit from the bound row, and the dimension's HNSW
// index is provisioned on demand.
func (d *DB) InsertCollection(ctx context.Context, id, name string, m *EmbeddingModel) (*Collection, error) {
	row := d.Pool.QueryRow(ctx, `INSERT INTO collections
		(id, name, embedding_model, vector_dim, embedding_model_id)
		VALUES ($1,$2,$3,$4,$5)
		RETURNING `+collectionCols,
		id, name, m.ModelID, m.VectorDim, m.ID)
	c, err := scanCollection(row)
	if err != nil {
		return nil, err
	}
	if err := d.EnsureDimIndex(ctx, m.VectorDim); err != nil {
		return nil, err
	}
	return c, nil
}

// CollectionCounts returns (doc_count, chunk_count) for one collection —
// the /v1/collections/{id}/stats payload.
func (d *DB) CollectionCounts(ctx context.Context, id string) (int, int, error) {
	var docCount int
	if err := d.Pool.QueryRow(ctx,
		`SELECT count(*) FROM documents WHERE collection_id = $1`, id).Scan(&docCount); err != nil {
		return 0, 0, err
	}
	var chunkCount int
	err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM chunks c
		JOIN documents d ON d.id = c.doc_id WHERE d.collection_id = $1`, id).Scan(&chunkCount)
	return docCount, chunkCount, err
}

// CollectionStatsAgg returns the extended stats readout: total raw bytes of
// the collection's documents (NULL byte_size rows contribute 0) and the sum
// of each doc's total_shards. The byte readout feeds the KB gallery cards;
// the shard readout shows pipeline load per KB.
func (d *DB) CollectionStatsAgg(ctx context.Context, id string) (byteSize int64, totalShards int64, err error) {
	err = d.Pool.QueryRow(ctx, `SELECT COALESCE(sum(byte_size), 0),
		COALESCE(sum(total_shards), 0) FROM documents WHERE collection_id = $1`, id).
		Scan(&byteSize, &totalShards)
	return byteSize, totalShards, err
}

// MCPDocCountPerCollection returns doc counts keyed by collection id — the
// list_collections tool input.
func (d *DB) MCPDocCountPerCollection(ctx context.Context) (map[string]int, error) {
	rows, err := d.Pool.Query(ctx, `SELECT id,
		(SELECT count(*) FROM documents WHERE collection_id = c.id)
		FROM collections c`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}
