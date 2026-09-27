package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// retryDB stubs the pool: each call to Tx consults the scripted failure
// sequence, then runs fn (so a retried fn re-runs, matching the real
// Tx→Begin/Commit shape). No PG involved — TxWithRetry's
// retry/backoff/classification contract is pure behavior around DB.Tx.
type retryDB struct {
	DB     // embedded: only Tx is overridden; the rest is unused here
	calls  int
	script []error // per-call outcomes; exhausted script → success
}

func (r *retryDB) Tx(ctx context.Context, fn func(pgx.Tx) error) error {
	i := r.calls
	r.calls++
	if i < len(r.script) {
		if err := r.script[i]; err != nil {
			return err
		}
	}
	return fn(nil)
}

func TestTxWithRetry(t *testing.T) {
	deadlock := &pgconn.PgError{Code: "40P01"}
	lockTimeout := &pgconn.PgError{Code: "55P03"}
	unique := &pgconn.PgError{Code: "23505"}
	plain := errors.New("boom")

	cases := []struct {
		name      string
		attempts  int
		script    []error
		wantCalls int
		wantErr   error // non-nil: error must match (wrapped for exhaustion)
		wantSleep int
	}{
		{
			name:      "two transient failures then success → 3 calls, 2 sleeps",
			attempts:  3,
			script:    []error{deadlock, lockTimeout},
			wantCalls: 3,
			wantSleep: 2,
		},
		{
			name:      "attempts exhausted → last transient error returned",
			attempts:  2,
			script:    []error{deadlock, deadlock},
			wantCalls: 2,
			wantErr:   deadlock,
			wantSleep: 1, // no sleep after the final attempt
		},
		{
			name:      "non-transient error → 1 call, no sleep, immediate return",
			attempts:  5,
			script:    []error{unique},
			wantCalls: 1,
			wantErr:   unique,
			wantSleep: 0,
		},
		{
			name:      "plain error → immediate return, no sleep",
			attempts:  5,
			script:    []error{plain},
			wantCalls: 1,
			wantErr:   plain,
			wantSleep: 0,
		},
		{
			name:      "attempts < 1 clamps to 1",
			attempts:  0,
			script:    []error{deadlock},
			wantCalls: 1,
			wantErr:   deadlock,
			wantSleep: 0,
		},
		{
			name:      "first-call success → 1 call, no sleep",
			attempts:  3,
			script:    nil,
			wantCalls: 1,
			wantSleep: 0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sleeps := 0
			prev := testRetrySleep
			testRetrySleep = func(d time.Duration) { sleeps++ }
			defer func() { testRetrySleep = prev }()

			db := &retryDB{script: tc.script}
			err := txWithRetryLoop(context.Background(), tc.attempts, func(tx pgx.Tx) error {
				return nil
			}, db.Tx)

			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("expected error, got nil (calls=%d)", db.calls)
				}
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error mismatch: %v (want %v)", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if db.calls != tc.wantCalls {
				t.Fatalf("call count drift: %d, want %d", db.calls, tc.wantCalls)
			}
			if sleeps != tc.wantSleep {
				t.Fatalf("sleep count drift: %d, want %d", sleeps, tc.wantSleep)
			}
		})
	}
}

// TestRetryBackoffShape pins the backoff curve: geometric from the 50ms
// base with a 2s cap, and jitter within ±25% of the unjittered value.
func TestRetryBackoffShape(t *testing.T) {
	wantByAttempt := map[int]time.Duration{
		1: 50 * time.Millisecond,
		2: 100 * time.Millisecond,
		3: 200 * time.Millisecond,
		4: 400 * time.Millisecond,
		5: 800 * time.Millisecond,
		6: 1600 * time.Millisecond,
		7: 2 * time.Second,
		8: 2 * time.Second, // capped
		9: 2 * time.Second, // capped
	}
	for attempt, want := range wantByAttempt {
		for i := 0; i < 50; i++ {
			got := retryBackoff(attempt)
			lo := want - want/4
			hi := want + want/4
			if got < lo || got > hi {
				t.Fatalf("retryBackoff(%d) = %v outside jitter window [%v, %v] around %v",
					attempt, got, lo, hi, want)
			}
		}
	}
}
