# anydoc-go — in-process CGO binding to the anydoc Rust crate

Lybrix 2.0 replicates Tencent WeKnora's exact CGO pattern: the Firecrawl
`anydoc` Rust crate is compiled to a static C archive and linked into the
`lybrix-server` parser for born-digital PDF/DOCX/PPTX/XLSX/TXT conversion,
in-process, at shard level (a whole 16–24 page shard per call, never
per-page requests).

## Layout

- `include/anydoc.h` — the extern "C" API (vendor via cbindgen, kept in tree).
- `lib/linux_amd64_gnu/libanydoc_go.a` — the static archive for the
  linux/amd64 (gnu libc) ingest hosts. Other targets get their own
  `lib/<target_triple>/` directory; `anydoc.go`'s LDFLAGS are per-GOOS/GOARCH.

## Building the lib

```bash
./scripts/build-anydoc-lib.sh path/to/anydoc-rust-checkout
```

The script runs `cargo build --release` on the vendored crate (crate-type
must include `staticlib`), regenerates the header with cbindgen, and copies
`libanydoc_go.a` into `lib/linux_amd64_gnu/`. The Rust crate itself is
vendored/cloned here upstream — it is NOT reimplemented.

## Building lybrix-server against it

```bash
# ingest host (lib present) — fast path linked:
go build -tags anydoc ./cmd/lybrix-server

# CI / any host without the lib — stub fallback, compiles clean:
go build ./cmd/lybrix-server
```

The `-tags anydoc` variant requires CGO; the stub variant is a pure-Go
build. Selection lives in the parser service wiring (`ANYDOC_ENABLED`), not
the call site: with the stub, `Available()` is false and every shard falls
through to the docling-serve HTTP fallback.

## Thread-safety notes

anydoc keeps per-thread error registers. Every CGO call is wrapped in
`runtime.LockOSThread()` / `defer runtime.UnlockOSThread()` so a Go
goroutine's conversion state can never leak into another goroutine running
on the same OS thread after a scheduling preemption.
