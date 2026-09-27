package store

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// Table test for the transient classifier (R-36): the three contention
// classes retry; everything else — constraint violations, cancellation,
// plain errors, nil — returns immediately.
func TestIsTransientPGError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "deadlock 40P01", err: &pgconn.PgError{Code: "40P01"}, want: true},
		{name: "serialization 40001", err: &pgconn.PgError{Code: "40001"}, want: true},
		{name: "lock not available 55P03", err: &pgconn.PgError{Code: "55P03"}, want: true},
		{name: "unique violation 23505", err: &pgconn.PgError{Code: "23505"}, want: false},
		{name: "query canceled 57014", err: &pgconn.PgError{Code: "57014"}, want: false},
		{name: "wrapped deadlock via fmt (errors.As chain)",
			err: fmt.Errorf("apply schema.sql: %w", &pgconn.PgError{Code: "40P01"}), want: true},
		{name: "context.Canceled", err: context.Canceled, want: false},
		{name: "plain error", err: errors.New("boom"), want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTransientPGError(tc.err); got != tc.want {
				t.Fatalf("IsTransientPGError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
