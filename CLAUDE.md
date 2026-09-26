# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Document ingestion & retrieval platform: self-hosted ingestion of large PDFs (300–800 pages) into a searchable corpus exposed via MCP, with an admin web UI. Everything runs on Docker Compose.

**`docs/prd.md` is the source of truth** (16 sections). **`docs/BACKLOG.md` is the incident ledger** — a new confirmed bug lands there as a row (symptom → root cause → fix commit) *before* any code change.

Central design constraint driving everything: **a book is never a unit of work — a 16–24 page shard is.** Every scaling, retry, progress, and memory decision follows from that.

## 2.0 — Go rewrite (branch `feat/lybrix-2.0-revamp`)

The 1.0 Python stack (FastAPI api, FastMCP mcp, four-entrypoint workers image, six shared libs, Qdrant) was replaced **in place** by a 100% Go backend per `LYBRIX_2.0_EVOLUTION_PLAN.md`:

- **One binary** (`cmd/lybrix-server`): `serve` (REST :8000 + MCP :8430 + janitor goroutine), `splitter`, `parser`, `embedder`, `janitor`, `keys bootstrap`, `migrate`, `rechunk` (rebuild chunks from the parsed S3 markdown without re-parsing — the corpus-migration path for a chunker change). Plus `cmd/lybrix-eval` (golden-set harness; same `scripts/eval/datasets/seed.jsonl`, same rank metrics).
- **ParadeDB replaces Qdrant**: PostgreSQL 17 + pgvector (HNSW, cosine, children only) + pg_search BM25, fused by weighted RRF in native SQL (`internal/store/search.go`; dense 1.0 / bm25 0.3, key-scope filter pushed into BOTH CTEs — never post-filter). `deploy/schema.sql` is embedded in the binary and applied idempotently at boot (advisory-lock guarded); the alembic chain is gone.
- **Two-tier parsing**: `third_party/anydoc-go` CGO fast path (`-tags anydoc`, `LockOSThread` around every call, shard-level units only) with a clean `!anydoc` stub for CI; `docling-serve` HTTP fallback for scanned shards or yield < 50 chars/page. Retry ladder preserved: attempt 2 quarter-split, 3 single-page, ≥4 text-only.
- **Hierarchical parent–child chunking** (`internal/chunker`) replaces flat 512-token windows: 384-rune children (76-rune overlap) under 2048-rune parents. Chunk text is a **verbatim slice of the stitched markdown** — tables, code fences, lists and paragraph breaks reach the index intact (R-26); protected regions are never split, an oversized table splits between rows, and a table's header row is re-injected into each following chunk. Heading breadcrumbs ride `ContextHeader`, prepended at embed time only, so stored text stays a clean source slice. Every parent and child carries a real page range derived from the stitch page map (R-27). Splitting is adaptive: a profiler picks heading → heuristic → legacy, and a validator falls through on a broken result.
- **Next.js web console unchanged** — the Go server satisfies the checked-in OpenAPI snapshot at `services/web/openapi.json` (25 paths, golden-tested against `internal/api/openapi_snapshot.json`) and serves it byte-identical at `/openapi.json`. Error envelope is FastAPI-style `{"detail": …}`.

## Commands

```bash
go build ./...        # or: make build
make test             # go test ./... (~all packages; store lane is testcontainers and skips without docker)
make lint             # golangci-lint run (line-length 100)
make vet              # go vet ./...

# eval (golden set, unchanged from 1.0):
go build -o bin/lybrix-eval ./cmd/lybrix-eval
LYBRIX_MCP_URL=... LYBRIX_MCP_TOKEN=... bin/lybrix-eval run \
  --dataset scripts/eval/datasets/seed.jsonl --top-k 8 --label baseline
bin/lybrix-eval compare scripts/eval/results/baseline-*.json scripts/eval/results/candidate-*.json

# anydoc fast path (ingest host, after scripts/build-anydoc-lib.sh):
go build -tags anydoc ./cmd/lybrix-server

make up-core          # paradedb + redis + lybrix-server serve + web
make up-ingest        # lybrix-splitter/parser×4/embedder/janitor + docling-serve + tei planes
make down-ingest      # stops consumers immediately
```

Go is pinned to 1.25 (`go.mod`). Config lives in `.env` (copy from `deploy/.env.example`); every value is read by `internal/config` and nothing else touches the environment. Store tests boot `paradedb/paradedb:17` via testcontainers and **skip cleanly when no docker socket exists** — the CI lane is DB-free. Queue tests run on miniredis.

Web UI (Next.js 15 App Router + Tailwind v4 + shadcn/ui, Biome lint — theme is "paper & press", dark-only, tokens in `app/globals.css`) has its own toolchain in `services/web/`: `npm run dev|build`, `npm run lint`, and `npm run generate-client` — regenerate the typed API client (`lib/client/`, generated code, never hand-edit) from `openapi.json` after changing the API contract: `curl http://localhost:8000/openapi.json > openapi.json && npm run generate-client`. Server components call the api directly via `API_URL`; browser calls ride the same-origin rewrite (`next.config.mjs`, baked at build time). Mutating browser calls go through session-gated proxies that hold bearer keys server-side: `/api/admin/*` (admin key) and `/api/playground` (MCP playground → real MCP server via `lib/mcp-proxy.ts`). See `docs/adr/0003-web-stack.md` for why api and web stay separate services.

CI (`.github/workflows/ci.yml`) runs `go test ./...` (non-CGO stub lane), golangci-lint, govulncheck, and per-image docker build validation on every push; `:edge` images publish from `main`, semver tags publish versioned images.

## Architecture

```
upload → doc.split → doc.parse (×N shards) → doc.embed → ready
```

- Each arrow is a **Redis Stream consumer group** (`rag-workers`); **ParadeDB/Postgres is the state of record, Redis is dispatch only.** If Redis is wiped, the janitor re-enqueues everything not in a terminal state. This is what makes the system resumable (crash loses ≤ 1 shard).
- **Two planes that never share an embedding server:** ingest plane (`lybrix-*` workers + `tei-ingest`, throughput-optimised) and query plane (MCP tools + `tei-query`, latency-optimised). TEI batches by total token count, so a shared TEI container lets a 65k-token ingest batch destroy query p99 — never collapse them into one.
- Retry/ack semantics live in **exactly one place**: `internal/worker/runner.go`. Every worker uses this generic consumer loop. Handlers raise taxonomy errors (`internal/errors`); retryability comes from the error table, never string matching. A failing job is never ACKed or retried in the loop — it stays in the Redis PEL, and the janitor's XAUTOCLAIM reclaim enforces the delivery cap (`max(5, max_shard_attempts+1)`, quarantine → DLQ events row → XACK/XDEL). The PEL's `times_delivered` is the single attempt counter.
- **Janitor** (`internal/worker/janitor.go`): 7-step pass — lease reaper, escalate ≥max attempts, split requeue with pending-doc dedup (no blind re-add on scan failure), XAUTOCLAIM reclaim + quarantine, settled-book embed rescue, stuck-doc warning, minute-bucket metrics rollup (`metrics_rollup`, one row per minute bucket windowed over `shards.done_at`).

### Repo layout

```
cmd/             lybrix-server (serve/splitter/parser/embedder/janitor/keys/migrate),
                 lybrix-eval (run/compare over the golden set)
internal/        config, errors, logging, queue (Redis Streams), objectstore (MinIO),
                 store (pgx pool + schema + queries + RRF search), api (chi REST),
                 mcp (mcp-go, six tools), pipeline (splitter/gate/anydoc/docling/
                 stitch/tei/embedder), chunker (verbatim-slice hierarchical
                 chunking: protected spans, table headers, strategy tiers),
                 service (worker handlers), worker (runner + janitor)
third_party/     anydoc-go — extern-C binding to the Firecrawl anydoc Rust crate
                 (staticlib via scripts/build-anydoc-lib.sh; stub build for CI)
deploy/          docker-compose (paradedb/redis/lybrix-server/web + ingest plane),
                 schema.sql, Dockerfile.lybrix, .env.example
scripts/         build-anydoc-lib.sh, eval/ (seed.jsonl + results)
services/web/    Next.js admin UI + MCP Playground (models manager, tabbed upload hub)
docs/            prd.md (source of truth), BACKLOG.md (incident ledger), adr/
```

### Status / milestone awareness

2.0 implementation status: the full Go rewrite is in place — REST API (25 paths, OpenAPI golden-tested), MCP server (6 tools, bearer auth, per-method usage rows), splitter/parser (two-tier + retry ladder + PDF cache; EPUB takes an explicit branch: single synthetic shard, docling-only, no sub-shard retry ladder)/embedder (hierarchical chunking + registry-driven batches), runner + janitor (7 steps incl. DLQ quarantine and metrics rollup), ParadeDB schema with per-dimension partial HNSW + pg_search BM25, and the Go eval harness. The anydoc `-tags anydoc` lane compiles; linking it requires the Rust static archive built on the ingest host. Remaining known gaps: the testcontainers store lane requires a docker socket (skips elsewhere); live E2E + resilience drills (kill-parser, Redis wipe, poison-job → DLQ) run after `make up-core && make up-ingest`. Check code + `docs/BACKLOG.md` before assuming a feature is real or missing.

2.0.1 — dynamic model registry + multi-source ingestion (branch `feat/dynamic-model-providers`): a DB-backed `embedding_models` registry replaces the hardcoded embed env vars (TEI / Ollama / OpenAI-compatible providers; arbitrary 1–2000 dims; per-dim partial HNSW indexes via `EnsureDimIndex`). Collections bind a model **mandatorily at creation** (`POST /v1/collections` requires `embedding_model_id`) and can rebind via `POST /v1/collections/{id}/model` (metadata sync only — no automated re-embed migration runner in v1; stale-dim vectors are excluded from dense search until re-embedded). Multi-source ingestion: `/upload` is a tabbed hub — Local files (presign), Remote URL (`POST /v1/documents/fetch-url`, disk-streamed with dial-time SSRF enforcement), OPDS connector (`opds/browse` + `opds/sync`, Basic Auth, credentials never persisted). Embed env vars (`EMBED_BACKEND`, `EMBED_MODEL`, `EMBED_DIM`, `TEI_INGEST_URL`, `TEI_QUERY_URL`, `EMBED_BATCH_SIZE`, …) are **first-boot seed values only** — manage models in the UI afterwards.

2.0.2 — WeKnora-style multi-collection search, default embedder retired (`internal/store/search.go`): search targets are grouped by their bound embedding model; the query is embedded **once per unique model** (`MultiHybridSearch`), each group contributes a dense leg at its own dimension, one BM25 leg spans all targets, and all legs fuse with weighted RRF (dense 1.0 / BM25 0.3, k=60 — same constants as single-collection `HybridSearch`). The key-scope allowlist is intersected **before** grouping so out-of-scope models are never embedded. The `is_default` column and `ErrModelDefault` are gone — the registry is a pure backend catalog; embed env vars only seed catalog rows at first boot.
