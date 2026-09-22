package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// ListCollections returns all collections.
func (d *DB) ListCollections(ctx context.Context) ([]*Collection, error) {
	rows, err := d.Pool.Query(ctx, `SELECT id, name, embedding_model, vector_dim, created_at
		FROM collections ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Collection
	for rows.Next() {
		var c Collection
		if err := rows.Scan(&c.ID, &c.Name, &c.EmbeddingModel, &c.VectorDim, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

// GetCollection fetches one collection by id; nil when absent.
func (d *DB) GetCollection(ctx context.Context, id string) (*Collection, error) {
	row := d.Pool.QueryRow(ctx, `SELECT id, name, embedding_model, vector_dim, created_at
		FROM collections WHERE id = $1`, id)
	var c Collection
	err := row.Scan(&c.ID, &c.Name, &c.EmbeddingModel, &c.VectorDim, &c.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// InsertCollection creates a collection (embedding model and dimension are
// set at creation and immutable — §8.1).
func (d *DB) InsertCollection(ctx context.Context, id, name, embeddingModel string, vectorDim int) (*Collection, error) {
	row := d.Pool.QueryRow(ctx, `INSERT INTO collections (id, name, embedding_model, vector_dim)
		VALUES ($1,$2,$3,$4)
		RETURNING id, name, embedding_model, vector_dim, created_at`,
		id, name, embeddingModel, vectorDim)
	var c Collection
	if err := row.Scan(&c.ID, &c.Name, &c.EmbeddingModel, &c.VectorDim, &c.CreatedAt); err != nil {
		return nil, err
	}
	return &c, nil
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
