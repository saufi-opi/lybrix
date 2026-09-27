package store

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// Transient PG error classes for the R-36 self-healing write paths. These
// are contention-shaped failures a bounded retry heals — a blip costs
// milliseconds, not a whole parse (R-36 §1.3 move 3):
//
//   - 40P01 deadlock_detected: two writers each hold what the other wants.
//   - 40001 serialization_failure: concurrent update serialization.
//   - 55P03 lock_not_available: lock_timeout expired waiting for a lock.
//
// Retries are internal to TxWithRetry/BootstrapSchema — invisible to the
// error taxonomy and the UI (the taxonomy is a wire contract; no new spec
// was added for these).
func IsTransientPGError(err error) bool {
	if err == nil {
		return false
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "40P01", "40001", "55P03":
			return true
		}
	}
	return false
}
