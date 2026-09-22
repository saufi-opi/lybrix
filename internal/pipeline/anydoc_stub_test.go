//go:build !anydoc

package pipeline

import (
	"context"
	"errors"
	"testing"
)

// Stub lane (no tag): the fast path reports unavailable; the parser falls
// through to docling. The tagged fixture lane runs only on the ingest host
// where libanydoc_go.a is present.
func TestAnyDocStubUnavailable(t *testing.T) {
	p := AnyDocParser{}
	if p.Available() {
		t.Fatal("stub must report unavailable")
	}
	_, err := p.Parse(context.Background(), ParseRequest{})
	if !errors.Is(err, ErrAnyDocUnavailable) {
		t.Fatalf("expected ErrAnyDocUnavailable, got %v", err)
	}
}

func TestTwoTierFallsThroughToDocling(t *testing.T) {
	// with the stub, selection wiring must pick docling for every shard
	tp := NewTwoTierParser("http://localhost:1", 50, 20, true)
	if tp.AnyDoc.Available() {
		t.Fatal("stub build must not select anydoc")
	}
}
