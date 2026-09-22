// Package worker: the generic consumer loop and the janitor — the single
// place retry/ack semantics live, mirroring 1.0's runner.py contract
// comment-for-comment.
package worker

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/saufi-opi/lybrix/internal/queue"
	"github.com/saufi-opi/lybrix/internal/store"
)

// Handler processes one job inside one DB transaction.
type Handler func(ctx context.Context, tx pgx.Tx, job map[string]any) error

// OnError records a failed job (events row with the taxonomy code).
type OnError func(ctx context.Context, tx pgx.Tx, job map[string]any, jobErr error)

// HandlerDeps carries what handlers need beyond the tx.
type HandlerDeps struct {
	DB    *store.DB
	Redis redis.Cmdable
}

// RunConsumer consumes forever: XREADGROUP → handler in one PG tx →
// ACK+XDEL on commit; error → on_error hook (events row w/ taxonomy code),
// entry stays in PEL; loop never dies on transient blips (5s backoff).
//
// There is NO DLQ in this loop. A failing job is logged, its on_error hook
// runs, and the entry stays unacked in the Redis PEL so XAUTOCLAIM can
// revisit it. The terminal path is enforced at the janitor reclaim
// (janitor step 4): an entry the reclaim sees at/over the delivery cap
// (DELIVERY_CAP = max(5, max_shard_attempts + 1) deliveries) is quarantined
// — one DLQ events row, then XACK + XDEL — instead of being re-added. This
// loop deliberately holds no attempt counter: the PEL's times_delivered is
// the single source of truth, so a Redis wipe also resets the cap (job
// state of record lives in Postgres, not Redis).
//
// recycleAfter (PARSER_RECYCLE_AFTER): exit cleanly once N jobs have been
// handled successfully (failures don't advance the count). The caller's
// process ends on a job boundary — after the ACK — so docker's restart
// policy revives it with fresh memory; nothing is lost. 0 disables
// recycling (consume forever).
func RunConsumer(ctx context.Context, db *store.DB, r redis.Cmdable, stream, consumer string, handler Handler, onError OnError, prefetch int, pollIdleMS int64, recycleAfter int) error {
	if prefetch < 1 {
		prefetch = 1
	}
	if pollIdleMS <= 0 {
		pollIdleMS = 5000
	}
	if err := queue.EnsureStreams(ctx, r, stream); err != nil {
		return err
	}
	slog.Info("consumer start", "stream", stream, "consumer", consumer)
	handled := 0
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if recycleAfter > 0 && handled >= recycleAfter {
			slog.Info("recycle: clean exit for process recycle",
				"handled", handled, "limit", recycleAfter)
			return nil
		}
		entries, err := queue.ReadJobs(ctx, r, stream, consumer, prefetch, pollIdleMS)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// never die on a transient Redis/PG blip; log and keep polling
			slog.Error("consumer loop error; retrying in 5s", "err", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
			continue
		}
		for _, entry := range entries {
			err := db.Tx(ctx, func(tx pgx.Tx) error {
				return handler(ctx, tx, entry.Job)
			})
			if err == nil {
				queue.Ack(ctx, r, stream, entry.ID)
				handled++
				continue
			}
			// handler decided failure; record + keep the message
			// unacked so XAUTOCLAIM/lease reaper can revisit it.
			slog.Error("job failed", "stream", stream, "entry", entry.ID, "err", err)
			if onError != nil {
				if err := db.Tx(ctx, func(tx pgx.Tx) error {
					onError(ctx, tx, entry.Job, err)
					return nil
				}); err != nil {
					slog.Error("on_error also failed", "err", err)
				}
			}
		}
	}
}

// RecordJobError is the default on_error: write an events row with the
// taxonomy code (runner.py record_job_error).
func RecordJobError(ctx context.Context, tx pgx.Tx, job map[string]any, jobErr error, stage, workerID string) {
	code := ""
	var docID *string
	var idx *int
	if pe, ok := jobErr.(interface{ Code() string }); ok {
		code = pe.Code()
	}
	if s, ok := job["doc_id"].(string); ok && s != "" {
		docID = &s
	}
	if f, ok := job["idx"].(float64); ok {
		i := int(f)
		idx = &i
	}
	_ = store.WriteEvent(ctx, tx, "error", stage, jobErr.Error(), docID, idx, codePtr(code), strPtr(workerID), nil)
}

func codePtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
