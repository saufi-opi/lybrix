package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// WriteEvent inserts one events row. Callers run it inside their own
// transaction so an event never describes a state change that failed to
// commit (1.0 events.py contract).
func WriteEvent(ctx context.Context, tx pgx.Tx, level, stage, message string, docID *string, shardIdx *int, code *string, workerID *string, context map[string]any) error {
	if context == nil {
		context = map[string]any{}
	}
	_, err := tx.Exec(ctx, `INSERT INTO events (doc_id, shard_idx, level, stage, code, message, context, worker_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		docID, shardIdx, level, stage, code, message, context, workerID)
	return err
}

// WriteEventPool is WriteEvent on the pool (no ambient tx) — fire-and-forget
// operational records that must land even outside a handler transaction.
func (d *DB) WriteEventPool(ctx context.Context, level, stage, message string, docID *string, shardIdx *int, code *string, workerID *string, context map[string]any) error {
	return d.Tx(ctx, func(tx pgx.Tx) error {
		return WriteEvent(ctx, tx, level, stage, message, docID, shardIdx, code, workerID, context)
	})
}

// EventFilter narrows the Logs page listing.
type EventFilter struct {
	DocID  string
	Level  string
	Stage  string
	Limit  int
	LastID int64 // > 0: only rows with id > LastID (SSE poll)
	Asc    bool  // SSE poll walks ascending; list walks created_at desc
}

// ListEvents returns the filtered event rows.
func (d *DB) ListEvents(ctx context.Context, f EventFilter) ([]*Event, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	q := `SELECT id, doc_id, shard_idx, level, stage, code, message, context, worker_id, created_at FROM events`
	args := []any{}
	where := []string{}
	if f.DocID != "" {
		args = append(args, f.DocID)
		where = append(where, "doc_id::text = $1")
	}
	if f.Level != "" {
		args = append(args, f.Level)
		where = append(where, "level = $2")
	}
	if f.Stage != "" {
		args = append(args, f.Stage)
		where = append(where, "stage = $3")
	}
	if f.LastID > 0 {
		args = append(args, f.LastID)
		where = append(where, "id > $4")
	}
	if len(where) > 0 {
		q += " WHERE "
		for i, w := range where {
			if i > 0 {
				q += " AND "
			}
			q += w
		}
	}
	if f.Asc {
		q += " ORDER BY id ASC"
	} else {
		q += " ORDER BY created_at DESC"
	}
	args = append(args, f.Limit)
	q += " LIMIT $5"
	rows, err := d.Pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Event
	for rows.Next() {
		var e Event
		var ctxJSON []byte
		if err := rows.Scan(&e.ID, &e.DocID, &e.ShardIdx, &e.Level, &e.Stage, &e.Code,
			&e.Message, &ctxJSON, &e.WorkerID, &e.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(ctxJSON, &e.Context)
		out = append(out, &e)
	}
	return out, rows.Err()
}

// SSEPayload is the exact per-event JSON the SSE stream emits
// (events.py:66-75 payload key-for-key).
type SSEPayload struct {
	ID        int64  `json:"id"`
	DocID     string `json:"doc_id"`
	ShardIdx  *int   `json:"shard_idx"`
	Level     string `json:"level"`
	Stage     string `json:"stage"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	CreatedAt string `json:"created_at"`
}

// NewSSEPayload maps an Event row onto the SSE wire shape; empty optional
// fields stay as their zero values exactly like the Python `str(x) if x
// else None` mapping.
func NewSSEPayload(e *Event) SSEPayload {
	p := SSEPayload{
		ID:        e.ID,
		ShardIdx:  e.ShardIdx,
		Level:     e.Level,
		Message:   e.Message,
		CreatedAt: e.CreatedAt.Format(time.RFC3339Nano),
	}
	if e.DocID != nil {
		p.DocID = *e.DocID
	}
	if e.Stage != nil {
		p.Stage = *e.Stage
	}
	if e.Code != nil {
		p.Code = *e.Code
	}
	return p
}

// EventDocID is a small helper for optional doc_id pointers.
func EventDocID(id string) *string { return &id }

// EventShardIdx is a small helper for optional idx pointers.
func EventShardIdx(i int) *int { return &i }

// EventCode is a small helper for optional code pointers.
func EventCode(c string) *string { return &c }

// EventWorkerID is a small helper for optional worker_id pointers.
func EventWorkerID(w string) *string { return &w }

var _ = time.Now
