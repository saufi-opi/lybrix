//go:build !anydoc

// The !anydoc twin of anydoc.go: the default CI build compiles this stub so
// the fast path is simply never selected (the gate falls through to
// docling-serve). `-tags anydoc` links the real Rust lib instead.
package pipeline

import "context"

// AnyDocParser is the CGO fast-path parser. In the stub build it exists
// with the same surface so selection wiring compiles unchanged.
type AnyDocParser struct{}

// Parse implements Parser with a typed unavailable error.
func (AnyDocParser) Parse(ctx context.Context, req ParseRequest) (ParseResult, error) {
	return ParseResult{}, ErrAnyDocUnavailable
}

// Available reports whether the fast path is linked in.
func (AnyDocParser) Available() bool { return false }
