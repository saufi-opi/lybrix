//go:build anydoc

// The real CGO fast path: WeKnora-identical in-process binding to the
// Firecrawl `anydoc` Rust crate, linked as a static archive. Built only
// with `-tags anydoc` (ingest-host builds); the default build compiles
// anydoc_stub.go instead so CI never needs the lib.
//
// Thread safety: the Rust side keeps thread-local error registers, so every
// CGO call is pinned to its OS thread with runtime.LockOSThread /
// UnlockOSThread (blueprint §4.1).
package pipeline

/*
#cgo CFLAGS: -I${SRCDIR}/../../third_party/anydoc-go/include
#cgo linux,amd64 LDFLAGS: -L${SRCDIR}/../../third_party/anydoc-go/lib/linux_amd64_gnu -lanydoc_go -lm -lstdc++
#include <stdlib.h>
#include "anydoc.h"
*/
import "C"

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"time"
	"unsafe"
)

// readFile loads a shard's bytes off disk (the CGO lane's file-input path).
func readFile(path string) ([]byte, error) { return os.ReadFile(path) }

// AnyDocParser is the in-process conversion engine.
type AnyDocParser struct{}

// Available reports whether the fast path is linked in.
func (AnyDocParser) Available() bool { return true }

// Parse converts one shard PDF to GitHub-flavored markdown in-process.
// Sub-100ms per shard is the expected steady state (blueprint §4.2).
func (AnyDocParser) Parse(_ context.Context, req ParseRequest) (ParseResult, error) {
	started := time.Now()

	src := req.PDFBytes
	if len(src) == 0 {
		b, err := readFile(req.PDFPath)
		if err != nil {
			return ParseResult{}, err
		}
		src = b
	}
	if len(src) == 0 {
		return ParseResult{}, fmt.Errorf("anydoc: empty input")
	}

	// runtime.LockOSThread: anydoc keeps error registers thread-local;
	// unpinned goroutines could leak conversion state across calls.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	format := C.int(req.DocFormat.AnydocCode())
	if int(format) < 0 {
		return ParseResult{}, fmt.Errorf("anydoc: format %d has no fast path", int(req.DocFormat))
	}
	var out *C.uchar
	var outLen C.size_t
	errBuf := (*C.char)(C.malloc(256))
	if errBuf == nil {
		return ParseResult{}, fmt.Errorf("anydoc: out of memory for error buffer")
	}
	defer C.free(unsafe.Pointer(errBuf))

	code := C.anydoc_convert((*C.uchar)(unsafe.Pointer(&src[0])),
		C.size_t(len(src)), format, &out, &outLen, errBuf)
	if code != 0 {
		detail := C.GoString(errBuf)
		return ParseResult{}, fmt.Errorf("anydoc: convert failed (%d): %s", int(code), detail)
	}
	if out == nil || outLen == 0 {
		// empty output → the yield check decides the fallback
		return ParseResult{Markdown: "", Engine: "anydoc"}, nil
	}
	md := C.GoBytes(unsafe.Pointer(out), C.int(outLen))
	C.anydoc_free_buffer(out)

	return ParseResult{
		Markdown:   string(md),
		Engine:     "anydoc",
		DurationMS: time.Since(started).Milliseconds(),
	}, nil
}
