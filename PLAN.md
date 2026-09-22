# Lybrix 2.0 — Go Rewrite Implementation Plan

**Role:** planner · **Branch:** `feat/lybrix-2.0-revamp` · **Date:** 2026-09-21
**Blueprint:** `LYBRIX_2.0_EVOLUTION_PLAN.md` (approved) · **Constraints from user:** Keep MinIO · port eval to Go · **immediate hard cut** (no parallel run — legacy Python stack is replaced in place, corpus re-ingested after cutover).
**First implementation step:** replace the obsolete `PLAN.md` at repo root (old review-fix plan, all its work landed per `PLAN_REVIEW.md`) with this document. No git commit/push — implementer and verifier commit separately.

---

## 0. Context

Lybrix 1.0 is a Python microservice stack (FastAPI api, FastMCP mcp, four-entrypoint workers image, six shared libs) over Qdrant + Postgres + MinIO + Redis Streams, with a Next.js 15 admin UI. The approved blueprint replaces the entire Python backend with one Go binary (`lybrix-server`) and eliminates Qdrant in favor of ParadeDB (PostgreSQL 17 + pgvector + pg_search BM25), while preserving:

- the **16–24 page shard** unit-of-work over Redis Streams (split → parse → embed, janitor-recovered),
- the **Next.js web console unchanged** (it must satisfy the checked-in OpenAPI snapshot at `services/web/openapi.json` — 18 paths),
- the **6-tool MCP surface** at `:8430/mcp` with bearer-key auth,
- the **error taxonomy / retry ladder / lease / DLQ** semantics that the incident ledger (`docs/BACKLOG.md`) was built from.

Everything below is verified against the actual repo at HEAD `fe67823`.

### 0.1 The contract surface the Go rewrite must reproduce (verified)

**REST routes (`services/web/openapi.json`, 18 paths)** — auth = `Authorization: Bearer ragk_…`, sha256-hashed against `api_keys.key_hash`, scope check ∈ {search, ingest, admin}, 401/403 semantics per `services/api/src/api/deps.py:56-81`:

| Route | Scope | Notes |
|---|---|---|
| POST `/v1/documents/presign` | ingest | → `{doc_id, upload_url}` (MinIO presigned PUT) |
| POST `/v1/documents/{doc_id}/commit` | ingest | 202; 409 duplicate; 429 backlog w/ `Retry-After`; server-side sha256+`%PDF-`+page-cap verify |
| GET `/v1/documents` | search | filters `state,collection,q`, `limit≤200`, ordered by `updated_at desc` |
| GET `/v1/documents/{doc_id}` | search | `DocumentOut` shape (schemas.py:30-44) |
| GET `/v1/documents/{doc_id}/shards` | search | `ShardOut` list (schemas.py:51-60) |
| POST `/v1/documents/{doc_id}/retry` | admin | body `{scope: shards|embed|full}` → `{id, retried}` |
| GET/POST `/v1/collections` | search/admin | 201 create, 409 dup id |
| GET `/v1/collections/{id}/stats` | search | `{id,name,embedding_model,doc_count,chunk_count}` |
| POST `/v1/search` | search | `{query, collection?, top_k≤25}` → hit array w/ `chunk_id,doc_id,doc_title,page_start,page_end,heading_path,text,score,partial` |
| GET/POST `/v1/keys`, POST `/v1/keys/{id}/revoke` | admin | raw key shown once at create; 422 unknown scopes |
| GET `/v1/usage/summary?period=today|24h|7d|30d` | admin | `{period,total,by_key[]}` |
| GET `/v1/events`, GET `/v1/events/stream` (SSE) | — (session-gated proxies upstream) | SSE = PG-poll loop, `data: {json}\n\n` per event |
| GET `/v1/system/health`, `/queues`, `/pipeline`, `/metrics` | — | health checks PG/Redis/tei-query (+embed backend); `/pipeline` lane semantics per system.py:107-280 |

Error envelope is FastAPI-style: `{"detail": "…"}` — the Go server must emit the same shape (web SDK runs `throwOnError` and surfaces `detail`).

**MCP (6 tools, `services/mcp/src/mcp_server/server.py`):** `search`, `get_chunk_context`, `read_pages`, `list_documents`, `get_document`, `list_collections`. Bounded (`READ_PAGES_MAX=30`, `SEARCH_MAX_TOP_K=25`), citation triple on every result, `partial:true` + note when doc completeness < 1.0, per-key collection scoping (`assert_collection_allowed` — 403 on out-of-scope), usage rows per method (`tools/call:<name>`), `/health` unauthenticated.

**Worker pipeline (`services/workers/src/workers/`):** streams `doc.split`/`doc.parse`/`doc.embed`, group `rag-workers`, payload `{"job": "<json>"}` with `schema_version:1` (contracts.py). Runner semantics (runner.py): handler runs in one DB tx → ACK only after commit; failure leaves the entry in the PEL; janitor's XAUTOCLAIM reclaim enforces the delivery cap (`max(5, max_shard_attempts+1)`), quarantining (DLQ `events` row + XACK/XDEL) beyond it; PARSER_RECYCLE_AFTER = clean exit on job boundary. Parser: claim_shard (atomic `UPDATE…WHERE state IN (pending,failed) RETURNING`, attempts++), retry ladder (2: quarter-split, 3: single-page, ≥4: text-only/table-structure-off), PDF cache dir, OCR gate (mean chars/page < 20), mark done → book_settled → enqueue embed. Embedder: stitch (boundary-heading dedupe, per-line page map) → chunk (512 tokens, heading paths, neighbour dedupe) → ON CONFLICT DO NOTHING insert (unique `(doc_id, chunk_hash)`) → token-budgeted TEI/Ollama batches (ctx budget 1900, batch 48, truncate 6000 chars, jittered backoff, circuit breaker) → doc ready/partial + completeness. Janitor: 7-step pass (lease reaper, escalate ≥max attempts, split requeue w/ dedup, XAUTOCLAIM reclaim + quarantine, settled-book embed rescue, stuck-doc warning, minute-bucket metrics rollup). Stream hygiene details that must survive the rewrite: `socket_timeout > block`, XDEL-on-ACK (trim-on-ack), lag=None fallback scan, "no blind re-add on scan failure".

### 0.2 Deliberate deltas from 1.0 (approved by blueprint + user answers)

| Area | 1.0 | 2.0 |
|---|---|---|
| Storage engine | Qdrant (dense+sparse) + Postgres | **ParadeDB only** — native SQL RRF over pgvector cosine + pg_search BM25 |
| Runtime | 3 Python services + libs | 1 Go binary, 4 subcommands |
| Parsing | in-process Docling (Python) | **anydoc CGO fast path** + docling-serve HTTP fallback |
| Chunking | flat 512-token windows | **hierarchical parent–child** (384-token children → 2048–4096-token parents, breadcrumb-prefixed) |
| BM25 | client-encoded Qdrant sparse | pg_search index, server-side |
| Eval harness | Python `scripts/eval` | Go `cmd/lybrix-eval` (same golden set JSONL, same metrics) |
| Deploy | api/mcp/workers images + qdrant | one `lybrix-server` image + `paradedb/paradedb` + `docling-serve`; **Qdrant removed** |
| Raw PDFs | MinIO | **MinIO (kept per user decision)** — blueprint's `filename/file_size` columns are additive, `source_uri` stays |
| Cutover | — | **hard cut** per user: swap stack, re-ingest corpus fresh; no dual-run window |

---

## 1. Phase 1 — Foundation (ParadeDB, schema, store, scaffolding)

### 1.1 Repo scaffolding

Create:
- `go.mod` — module `github.com/saufi-opi/lybrix`, Go 1.23. Deps: `github.com/jackc/pgx/v5` (+`pgxpool`, `pgtype`), `github.com/redis/go-redis/v9`, `github.com/go-chi/chi/v5`, `github.com/mark3labs/mcp-go`, `github.com/pdfcpu/pdfcpu`, `github.com/aws/aws-sdk-go-v2` (+`feature/s3/manager`, creds `config`), `github.com/testcontainers/testcontainers-go` (test-only), `github.com/stretchr/testify` (test-only). CGO enabled only for the anydoc build tag.
- `Makefile` — replace `test`/`lint` targets: `go test ./...`, `go vet ./...`, `golangci-lint run`; drop `uv` targets; compose targets unchanged.
- `.golangci.yml` — errcheck, govet, staticcheck, revive (line-length 100).
- `deploy/Dockerfile.lybrix` — multi-stage: `golang:1.23-bookworm` builder (CGO when the anydoc lib is present; plain build otherwise) → `debian:bookworm-slim` runtime with `ca-certificates`. Two image variants is **not** needed: default build compiles the anydoc package with a stub fallback; `-tags anydoc` links the real lib (see 3.3). CI builds the default variant; the GPU/ingest overlay may use `-tags anydoc`.
- `.github/workflows/ci.yml` — rewrite: `go test ./...` (non-CGO lane), `golangci-lint`, `govulncheck`, docker build for `lybrix-server` + `web`; drop pip-audit/pytest/ruff/uv steps.

### 1.2 Compose changes

Modify `deploy/docker-compose.yml`:
- **Remove:** `qdrant`, `api`, `mcp`, `worker-janitor`, `migrate` (alembic), all `worker-*` Python services, `tei-rerank` stays (still useful later) — actually keep `tei-rerank` as-is.
- **Add `paradedb`:** image `paradedb/paradedb:17` (PG17 + pgvector + pg_search), same ports/env/healthcheck pattern as the old `postgres`, volume `paradedbdata`.
- **Add `docling-serve`:** image `quay.io/docling-project/docling-serve:latest`, port `:5001`, on the **ingest** profile, `mem_limit` 8g.
- **Add `lybrix-server`:** one image, profile `core`, runs `serve` (REST :8000 + MCP :8430 + janitor goroutine). Profile `ingest` runs `lybrix-server splitter|parser|embedder` replicas (replacing `worker-splitter`, `worker-parser{,-2..4}`, `worker-embedder` — same env/mem/`PARSER_RECYCLE_AFTER`/pdf-cache volume layout).
- Env blocks (`x-core-store-env` / `x-ingest-env`): drop `QDRANT_URL/QDRANT_API_KEY`; `DATABASE_URL` becomes a plain pgx DSN `postgres://rag:$PW@paradedb:5432/rag`; add `DOCLING_URL=http://docling-serve:5001`; keep everything else verbatim (Redis, MinIO, TEI, MCP_API_KEY, web env, traefik labels, Caddy).
- `deploy/.env.example` (new; current `.env.example` stays until cutover): drop `QDRANT_API_KEY`; add `DOCLING_URL`, `ANYDOC_ENABLED=true`.
- `deploy/minio/` init job unchanged.
- **MinIO profile placement unchanged from 1.0:** MinIO stays on the **ingest** profile (it runs on the ingest host next to the parsers that download full source PDFs per shard; the api/web/ui reach it over tailscale via `S3_PUBLIC_ENDPOINT`). Consequence for every acceptance step below that needs object storage: use `make up-core && make up-ingest`, not `make up-core` alone.

### 1.3 SQL schema — `deploy/schema.sql` (embedded in the Go binary, applied idempotently at boot)

Single file, executed by `lybrix-server` before serving (replaces the alembic `migrations/` chain; hard cut = fresh DB, no data migration). It is the blueprint §3 schema **extended with every column the OpenAPI contract reads**:

- Extensions: `vector`, `pg_search`.
- `collections(id TEXT PK, name TEXT, embedding_model TEXT DEFAULT 'BAAI/bge-m3', vector_dim INT DEFAULT 1024, created_at)` — the blueprint omits it; the UI's `/v1/collections` needs it.
- `documents(id UUID PK, collection_id TEXT REFERENCES collections(id), title TEXT, author TEXT, filename TEXT, byte_size BIGINT, source_uri TEXT NOT NULL, content_sha256 CHAR(64) NOT NULL, page_count INT, mime_type TEXT DEFAULT 'application/pdf', state TEXT CHECK IN ('uploaded','splitting','parsing','embedding','indexing','ready','failed','partial','archived') DEFAULT 'uploaded', error_code TEXT, error_detail TEXT, total_shards INT, shards_done INT DEFAULT 0, shards_failed INT DEFAULT 0, chunk_count INT, completeness NUMERIC(5,4), metadata JSONB DEFAULT '{}', uploaded_by TEXT, created_at, updated_at, ready_at, UNIQUE(collection_id, content_sha256), INDEX(state), INDEX(updated_at))`.
  - **Column is `byte_size`, not the blueprint's `file_size`** — it must match the 1.0 schema and the `DocumentOut.byte_size` field in the OpenAPI contract (services/api/src/api/schemas.py:38); `author` likewise maps 1:1 to `DocumentOut.author` — no rename, no remap in the web client.
- `shards(doc_id UUID REF documents ON DELETE CASCADE, idx INT, page_start INT, page_end INT, state TEXT CHECK IN ('pending','running','done','failed','skipped') DEFAULT 'pending', attempts INT DEFAULT 0, needs_ocr BOOL DEFAULT false, mean_chars_per_page REAL, parsed_uri TEXT, worker_id TEXT, lease_until TIMESTAMPTZ, duration_ms INT, peak_rss_mb INT, done_at TIMESTAMPTZ (indexed), error_code, error_detail, created_at, PK(doc_id, idx))`.
- `chunks(id UUID PK, doc_id UUID REF CASCADE, collection_id TEXT NOT NULL, chunk_hash CHAR(64) NOT NULL, parent_id UUID REF chunks(id), is_parent BOOL NOT NULL DEFAULT false, seq INT NOT NULL, page_start INT, page_end INT, heading_path TEXT[], header_breadcrumb TEXT, text TEXT NOT NULL, token_count INT NOT NULL, embedded_at TIMESTAMPTZ, created_at, UNIQUE(doc_id, chunk_hash))`.
  - Indexes: `hnsw (embedding vector_cosine_ops) WHERE is_parent = false` — **HNSW requires the column to exist at index time**, so `embedding vector(1024)` lives on `chunks` (NULL for parents) exactly as the blueprint writes it; the partial index is created after `CREATE EXTENSION vector`.
  - `CALL paradedb.create_bm25(index_name => 'chunks_bm25_idx', table_name => 'chunks', key_field => 'id', text_fields => '{"text": {}, "header_breadcrumb": {}}')` — applied in a `schema.sql` step guarded to run once (pg_search is assertion-based; wrap in DO block checking `paradedb.schema_version()`/pg_class, or issue and swallow "already exists").
- `events(id BIGSERIAL PK, doc_id UUID, shard_idx INT, level TEXT(8), stage TEXT(16), code TEXT, message TEXT, context JSONB DEFAULT '{}', worker_id TEXT, created_at, INDEX(doc_id, created_at), INDEX(level, created_at))`.
- `api_keys(id UUID PK, name TEXT, key_hash CHAR(64) UNIQUE, scopes TEXT[] DEFAULT '{search}', collections TEXT[] DEFAULT NULL, expires_at TIMESTAMPTZ, revoked_at TIMESTAMPTZ, last_used_at TIMESTAMPTZ, created_at)` — note 2.0 keeps **both** `revoked_at` (1.0 semantics) and `expires_at`; the blueprint's boolean `revoked` is replaced by the timestamp (contract `KeyOut` exposes `revoked_at`).
- `key_usage(id BIGSERIAL PK, api_key_id UUID REF CASCADE, surface TEXT ('mcp'|'api'), action TEXT, created_at, INDEX(api_key_id, created_at))` — 1.0 shape (surface+action), not the blueprint's `endpoint/status_code`; the UI's usage summary groups by key.
- `metrics_rollup(bucket TIMESTAMPTZ PK, pages_parsed, shards_done, shards_failed, chunks_embedded, parse_p50_ms, parse_p95_ms, peak_rss_p95_mb, queue_depth JSONB, search_p95_ms, search_count)`.
- Bootstrap note in `.env.example`: create the first admin key via `lybrix-server keys bootstrap` subcommand (new; replaces the Python one-liner).

### 1.4 Go packages to create in Phase 1

```
internal/config/config.go        env parsing (envconfig-style via std flag/env), mirrors libs/core config.py:
                                 DATABASE_URL, REDIS_URL, S3_ENDPOINT/ACCESS_KEY/SECRET_KEY, S3_BUCKET_RAW/PARSED,
                                 TEI_INGEST_URL/TEI_QUERY_URL, EMBED_BACKEND(tei|ollama), EMBED_MODEL, EMBED_DIM=1024,
                                 EMBED_BATCH_SIZE=48 (code default, matching config.py:47; the compose ingest env overrides
                                 it to 16 — keep that override on the lybrix-server ingest services), EMBED_CTX_BUDGET=1900,
                                 EMBED_TRUNCATE_CHARS=6000, EMBED_QUERY_PREFIX,
                                 SHARD_PAGES=20, OCR_MIN_CHARS_PER_PAGE=20, PARSER_RECYCLE_AFTER, PARSER_SOFT_RSS_MB→(kept: Go GC
                                 needs no soft budget; retained only for compose parity, documented as a no-op),
                                 SHARD_LEASE_SECONDS=600, PARSER_PDF_CACHE_DIR, MAX_PARSE_BACKLOG=2000 (code default, matching
                                 config.py:116; the compose api service overrides it to 20000 — keep that override on the
                                 lybrix-server core service), MAX_DOCUMENT_PAGES=800,
                                 JANITOR_INTERVAL_S=30, STUCK_MINUTES=60, MAX_SHARD_ATTEMPTS=4, SEARCH_DEFAULT_TOP_K=8,
                                 SEARCH_MAX_TOP_K=25, READ_PAGES_MAX=30, RERANK_ENABLED/TEI_RERANK_URL/RERANK_CANDIDATES=30/
                                 RERANK_TIMEOUT_S=5, DOCLING_URL, LOG_LEVEL, HTTP API/MCP bind addrs.
internal/errors/errors.go        ErrorCode enum (10 codes, exact string values from libs/core/errors.py), retryable map,
                                 Error struct {Code, Detail, Retryable} implementing error.
internal/store/                  pgxpool wrapper + query layer:
    db.go          NewPool(ctx, dsn), schema bootstrap (embed schema.sql, advisory-lock guarded), Tx helper.
    models.go      Document, Shard, Chunk, Collection, ApiKey, Event, KeyUsage, MetricsRollup + DocState/ShardState enums.
    documents.go   GetDocument, FindDuplicate, SetDocState, InsertShards, NextShardIdx, SkipShard, ClaimShard (atomic
                   UPDATE…WHERE state IN (pending,failed) RETURNING, attempts++ + lease_until), MarkShardDone (done +
                   shards_done bump in one tx), MarkShardFailed, BookSettled, RequeueExpiredLeases, ListDocuments
                   (state/collection/q ilike/limit/offset), GetShards, DeleteDocument(+chunks cascade).
    chunks.go      InsertChunks (COPY + ON CONFLICT (doc_id,chunk_hash) DO NOTHING), CountChunks, ReadPageChunks,
                   ChunkNeighbours (±window by seq), InsertParent/Child variants for the hierarchical chunker.
    keys.go        CreateKey, ListKeys, RevokeKey, AuthenticateKey (by sha256 hash, revoked/expired checks),
                   RecordUsage, UsageSummary(period cutoff SQL), TouchLastUsed.
    events.go      WriteEvent, ListEvents, EventFeed (poll loop source for SSE).
    search.go      HybridSearch (the blueprint §5 RRF SQL verbatim + key-scope `collection_id = ANY($6)` filter
                   pushed into BOTH CTEs — never post-filter, R-14), DeleteDocChunks, MetricsSnapshot.
    janitor.go     RequeueExpiredLeases, EscalateStuckShards, SettledUnenqueuedDocs, MetricsRollupUpsert.
internal/queue/streams.go        Redis Streams: stream names doc.split/doc.parse/doc.embed, group "rag-workers",
                                 EnsureStreams, XAddJob ({"job": json, schema_version:1}), ReadJobs (XREADGROUP ">",
                                 unparseable → ACK+XDEL), Ack (ACK + best-effort XDEL — trim-on-ack), XAutoClaim,
                                 UndeliveredCount (lag + None-fallback XRANGE scan), QueueDepth, Quarantine,
                                 PendingJobDocIDs (undelivered tail + PEL scan), consumer naming.
internal/queue/contracts.go      SplitJob{DocID, SourceURI}, ParseJob{DocID, Idx, PageStart, PageEnd, SourceURI},
                                 EmbedJob{DocID}, SchemaVersion=1, JSON tags identical to 1.0.
internal/objectstore/s3.go       MinIO client, presign_put, get/put/download helpers, raw_key/parsed_key —
                                 key layout copied exactly from libs/core/src/core/storage/s3.py:31-38 (verified):
                                 raw key = `{doc_id}.pdf` in bucket `raw`; parsed key = `{doc_id}/{shard_idx}.md`
                                 in bucket `parsed` — the bucket name carries the raw/parsed split, there is NO
                                 `raw/` or `parsed/` path prefix on the keys.
internal/logging/logging.go      slog setup honoring LOG_LEVEL; slog is the 1:1 replacement for std logging.
```

### 1.5 Phase 1 tests (`go test ./internal/...`) and acceptance

- `internal/config/config_test.go` — defaults, validation (top_k ordering, batch≥1).
- `internal/errors/errors_test.go` — codes string-match 1.0 values; retryable map matches `ERROR_SPECS`.
- `internal/queue/streams_test.go` — miniredis or live-Redis unit tests: XADD/read/ack/XDEL, undelivered-count fallback, quarantine writes DLQ event.
- `internal/store/*_test.go` — testcontainers-go with `paradedb/paradedb:17`: schema bootstrap idempotent (run twice), ClaimShard atomicity (two concurrent claims → one winner), BookSettled, dedupe unique constraint, hybrid-search SQL shape (fixture rows → expected RRF ordering), key auth (revoked/expired/missing-scope → typed errors).
- **Acceptance:** `go test ./internal/config/... ./internal/errors/... ./internal/queue/... ./internal/store/...` green; `make up-core && make up-ingest` boots paradedb+redis (core) and minio (ingest profile, per §1.2) and a `lybrix-server serve` that answers `/v1/system/health` with `{"status":"ok"}` (postgres+redis ok, tei fields per probe).

---

## 2. Phase 2 — Core server: REST API + MCP (Go)

### 2.1 `internal/api/` (chi router, port 8000)

```
internal/api/server.go       chi router assembly, middleware chain, graceful shutdown.
internal/api/middleware.go   BearerAuth(scope) → ApiKey in ctx; logs `{"detail"}` error envelope (401/403/404/409/
                             429/422 parity with deps.py); request-logging + panic-recovery; usage recording
                             (last_used_at + key_usage row, best-effort, never fails the request).
internal/api/documents.go    presign/commit/list/get/shards/retry — commit reproduces the streaming verify:
                             MinIO GET → `%PDF-` magic → sha256 → page-count probe (pdfcpu) → 400s; backlog gate
                             (QueueDepth > MAX_PARSE_BACKLOG → 429 + Retry-After: 60); dedupe 409; XADD split job.
internal/api/collections.go  list/create/stats.
internal/api/search.go       POST /v1/search — tei-query embed (with EMBED_QUERY_PREFIX), store.HybridSearch with
                             key-scope collection filter, response mapping incl. `partial`.
internal/api/keys.go         create (raw shown once, "ragk_"+32-byte urlsafe), list, revoke; 422 unknown scopes;
                             audit events rows (created/revoked) exactly like keys.py.
internal/api/usage.go        summary with today|24h|7d|30d cutoff semantics.
internal/api/events.go       GET /v1/events (+filters); GET /v1/events/stream — SSE via http.Flusher, PG-poll
                             loop (`poll_s` query param, default 2s), payload key-for-key with events.py:66-75.
internal/api/system.go       /health (pg, redis, tei-query probes → status ok|degraded), /queues, /pipeline
                             (lane semantics: waiting = undelivered+PEL, stale>15min, consumers idle<5min;
                             counts incl. docs_awaiting_embed/docs_parsing_active; in-flight shards; NO
                             qdrant_points — replaced by `chunks_total` count from ParadeDB), /metrics
                             (JSON counters snapshot; in-process atomic counters + janitor rollup read).
internal/api/openapi.go      serve the checked-in contract: embed services/web/openapi.json at /openapi.json
                             (byte-identical snapshot keeps `npm run generate-client` deterministic).
```

**Contract discipline:** every handler is written against `services/web/openapi.json` (18 paths, request/response field names, status codes). A golden test (`internal/api/openapi_test.go`) walks the snapshot's paths and asserts each route exists with matching methods; response structs get JSON-field golden tests against the snapshot's schema examples.

### 2.2 `internal/mcp/` (mcp-go, port 8430)

```
internal/mcp/server.go       mcp-go server, StreamableHTTP at /mcp; bearer middleware (same store.AuthenticateKey)
                             returning real HTTP 401 for non-/health paths (parity with McpAuthMiddleware);
                             ContextVar equivalent = ctx value carrying ApiKey; per-method key_usage rows
                             (action = method, "tools/call:<name>" refinement); /health unauthenticated.
internal/mcp/tools_search.go     search — clamp top_k, embed via tei-query, hybrid_search, citation triple,
                                 partial flag + note, collection-scope assert (403-equivalent MCP error).
internal/mcp/tools_reading.go    get_chunk_context (±window by seq), read_pages (cap READ_PAGES_MAX, joined markdown,
                                 truncated_to).
internal/mcp/tools_docs.go       list_documents (limit≤200, filters), get_document (metadata+chunk_count),
                                 list_collections (doc_count).
```

Tool descriptions copy the 1.0 strings verbatim (they are part of the agent-facing contract; the Playground UI renders them).

### 2.3 `cmd/lybrix-server/main.go`

Subcommands: `serve` (API + MCP + janitor goroutine in one process), `splitter`, `parser`, `embedder` (worker loops for the ingest profile), `janitor` (standalone, compose parity), `keys bootstrap`, `migrate` (schema.sql apply, for the compose `migrate`-style one-shot if preferred over boot-time apply). `--tags anydoc` linkage is compile-time, not runtime.

### 2.4 Phase 2 tests & acceptance

- `internal/api/*_test.go` — httptest against the router with a testcontainer ParadeDB + miniredis: full route parity (status codes, envelopes, 401/403/409/422/429), commit verify path with a fixture PDF served from a fake S3 (testcontainers MinIO), retry scopes' XADD behavior (shards→ParseJobs per failed shard + shards_failed counter unwind — the R-11 semantics from documents.py:220-258 must be preserved exactly).
- `internal/mcp/*_test.go` — tool impls against fakes + DB fixtures: clamps, caps, scoping, partial flag, usage rows.
- **Acceptance:** `go test ./internal/api/... ./internal/mcp/...` green; `make up-core && make up-ingest` with the new image → `curl /v1/system/health` ok; the existing web UI (`npm run dev` with `API_URL` pointed at the Go server) renders dashboard/documents/collections/pipeline/keys/usage/logs pages against live data; MCP playground round-trips `tools/list` + `search`.

---

## 3. Phase 3 — Pipeline & parsers (Go)

### 3.1 `internal/pipeline/splitter.go` — pdfcpu shard splitter

`Split(ctx, srcPath) (Bounds, error)`: page count via pdfcpu; outline extraction via pdfcpu bookmarks (7-bit + UTF-16 title decode; tolerant — any error → `[]`, fixed bounds fallback, per splitter.py:99-118). `FixedBounds(pageCount, shardPages, overlap=1)` and `ChapterAlignedBounds(outline, pageCount, shardPages)` are line-by-line ports of `libs/parsing/src/parsing/splitter.py` (the anti-loop guard at :44-45 and the degenerate-outline fallback at :94-95 are mandatory — they were both bugfixes). Writes shard rows + fans out `doc.parse` jobs; idempotency probe on `(doc_id, 0)` per splitter.py:32-40.

### 3.2 `internal/pipeline/gate.go` — OCR/text-density gate

Port of `ocr_gate.py`: per-page text-layer char counts (pdfcpu `ExtractText` per page over the shard's page range — C-go pdfium binding `github.com/gen2brain/go-fzumabin`/`pdfium` is **not** needed; pdfcpu text extraction is pure Go and sufficient for a mean-chars threshold), `mean < OCR_MIN_CHARS_PER_PAGE(20) → needs_ocr`, persist `mean_chars_per_page` on the shard row (new column, already in schema 1.3).

### 3.3 `third_party/anydoc-go/` + `internal/pipeline/anydoc.go` — CGO fast path

```
third_party/anydoc-go/
  include/anydoc.h          extern "C" API: anydoc_convert(buf*, len, format, out_buf**, out_len, err_buf)
  lib/linux_amd64_gnu/libanydoc_go.a   built by scripts/build-anydoc-lib.sh (cargo build --release → cbindgen
                            header → ar). The Rust crate is vendored/cloned here (Firecrawl anydoc), NOT reimplemented.
  README.md                 build + vendoring instructions, thread-safety notes.
scripts/build-anydoc-lib.sh Cargo → staticlib → copy .a into lib/<target_triple>/; cbindgen → include/.
```

`internal/pipeline/anydoc.go`:
```go
//go:build anydoc
// #cgo CFLAGS: -I${SRCDIR}/../../third_party/anydoc-go/include
// #cgo linux,amd64 LDFLAGS: -L${SRCDIR}/../../third_party/anydoc-go/lib/linux_amd64_gnu -lanydoc_go -lm -lstdc++
// #include "anydoc.h"
import "C"

type AnyDocParser struct{}
func (AnyDocParser) Parse(ctx context.Context, req ParseRequest) (ParseResult, error) {
    runtime.LockOSThread(); defer runtime.UnlockOSThread()   // thread-local error registers (blueprint §4.1)
    …C.anydoc_convert… → markdown bytes
}
```
A `//go:build !anydoc` twin (`anydoc_stub.go`) returns `ErrAnyDocUnavailable` so the default CI build compiles and the fast path is simply never selected (gate falls through to docling). Selection lives in the parser service wiring, not the call site.

### 3.4 `internal/pipeline/docling_client.go` — docling-serve fallback

HTTP client for `POST {DOCLING_URL}/v1/convert/file` (multipart: file + `body` JSON options `{"to_formats":["md"],"do_ocr":true,"do_table_structure":…}`), response `{"document":{"md_content": …}}`. Resilience identical to `libs/embedding/client.py`: jittered exponential backoff on 429/503/transport, circuit breaker after 5 consecutive failures → typed retryable `OCR_FAILED`/`EMBED_UNAVAILABLE`-class error, non-retryable 4xx fail-fast. Timeout per shard: 15–20s expected → client timeout 120s, context-cancellable.

### 3.5 `internal/pipeline/parser.go` — the two-tier flow + retry ladder

```
ParseRequest{PDFBytes|Path, PageStart, PageEnd, Attempt, ShardPages}
ParseResult{Markdown string, NeedsOCR bool, MeanCharsPerPage float64, DurationMS, PeakRSSMB, Engine ("anydoc"|"docling")}
```
Flow (blueprint §4.2): OCR-gate first (cheap) → born-digital → anydoc in-process (shard-level, never per-page) → yield check `len(md)/pages ≥ 50` chars → accept; else scanned/complex → docling-serve whole-shard multipart POST. Retry ladder ported exactly from parser.py:44-53 + `_split_and_requeue` (:56-98): attempt 2 → quarter-split (`overlap=0`, disjoint tiling — mandatory comment), 3 → single-page, ≥4 → text-only (docling with `do_table_structure=false`); parent shard → `skipped`, sub-shard idx continues after parent (NextShardIdx), `total_shards` grows, `SHARD_RESPLIT` event. PDF cache (`PARSER_PDF_CACHE_DIR`, atomic os.replace store, copy-not-link) ported. Parser consumer: prefetch=1, recycle_after=PARSER_RECYCLE_AFTER (clean exit on job boundary), claim→ladder→parse→upload markdown→MarkShardDone→book_settled→enqueue embed. `SHARD_OOM`/`SHARD_TIMEOUT` taxonomy preserved; Go GC makes SoftOOM moot but the RSS recording stays (peak via `runtime.MemStats`/cgroup read).

### 3.6 `internal/pipeline/chunker.go` — hierarchical parent–child

New algorithm (replaces flat 512-window `chunking/hybrid.py`):
```
type ChildChunk {Text, Seq, ChunkHash, TokenCount, ParentSeq, PageStart, PageEnd, HeadingPath []string, Breadcrumb string}
type ParentChunk {Text, Seq, ChunkHash, TokenCount, PageStart, PageEnd, HeadingPath []string}
ChunkHierarchical(markdown string, linePageMap []int, tok Tokenizer) (parents []ParentChunk, children []ChildChunk)
type Tokenizer interface{ Count(string) int }   // impls: WhitespaceTokenizer (default), optional HF-backed later
```
- Sections from markdown ATX headings (fence-aware `_sections` port of hybrid.py:117-163 — same heading-path stack semantics).
- **Parent** = one section's text snapped to headings, capped at 2048 tokens: an over-budget section is split at sub-heading boundaries, else hard-cut at 4096; `is_parent=true`, no embedding, carries `heading_path` + full text.
- **Child** = sliding window inside its parent: 384 tokens, 64-token stride overlap; each child stores `parent_id` (set post-insert by parent seq), `header_breadcrumb = "Doc Title > Chapter > Section"` (rendered from heading stack + doc title), `heading_path[]`, per-window page range (R-13 per-line page map ported from hybrid.py:59-84 — windows must carry their own line numbers, not the section's span).
- Child text passed to the embedder is **breadcrumb-prefixed** (`"[Doc > Chapter > Section] " + text`) per blueprint §5; stored text stays unprefixed, prefix applied at embed time (keeps read_pages/get_chunk_context output clean and chunk_hash stable across prefix changes).
- `drop_duplicate_neighbours` ported (dedupe adjacent identical hashes from shard overlap).
- Hash: sha256 of whitespace-normalized text, as 1.0.

### 3.7 `internal/pipeline/embedder.go` — TEI/Ollama batch embedder

Port of `libs/embedding/client.py` + `embedder.py`: TeiClient (backend tei|ollama, `/embed` vs `/api/embed` shape normalization, truncate_chars, jittered backoff 0.5→30s cap, breaker threshold 5 → retryable `EMBED_UNAVAILABLE`), token-budgeted batching (`PlanBatches` port of embedder.py:60-86, budget EMBED_CTX_BUDGET=1900, batch size 48; estimated token count = whitespace-token count — document that 1.0 used the HF tokenizer and 2.0 uses the whitespace heuristic that the chunker already standardized on; the budget exists to protect the backend, and the 6000-char truncate is the hard backstop). Embed flow: load done shards (page_start order) → parallel S3 prefetch (8 workers) → stitch (boundary-heading dedupe + per-line page map, stitch.py port) → `ChunkHierarchical` → insert parents + children (`ON CONFLICT DO NOTHING`) → embed children in batches → `UPDATE chunks SET embedding = $vec` batched by COPY into a temp table → doc ready/partial + completeness + chunk_count. Embed failure cap (Redis counter `embed:retries:{doc_id}`, 6h expiry, EMBED_MAX_ATTEMPTS=5 → FAILED terminal) ported verbatim.

### 3.8 `internal/worker/` — runner + janitor

```
internal/worker/runner.go    runConsumer(stream, consumer, handler, prefetch, blockMs, recycleAfter): XREADGROUP →
                             handler in one PG tx → ACK+XDEL on commit; error → on_error hook (events row w/ taxonomy
                             code), entry stays in PEL; loop never dies on transient blips (5s backoff). This is the
                             single place retry/ack semantics live, mirroring runner.py's contract comment-for-comment.
internal/worker/janitor.go   janitorPass 7 steps ported 1:1 from janitor.py (reaper → escalate → split requeue w/
                             pending_job_doc_ids dedup + no-blind-add-on-scan-failure → XAUTOCLAIM reclaim w/ delivery
                             cap max(5, MAX_SHARD_ATTEMPTS+1) + quarantine → settled-book embed rescue → stuck-doc
                             warning → minute-bucket metrics rollup w/ pct() from shards.done_at). Interval JANITOR_INTERVAL_S.
```

### 3.9 Phase 3 tests & acceptance

- `internal/pipeline/splitter_test.go` — fixed/chapter-aligned bounds vs 1.0 fixtures (port `tests/test_splitter.py` cases verbatim, incl. shard_pages=1 anti-loop and degenerate outline).
- `internal/pipeline/gate_test.go` — fixture PDFs (text-layer page, scanned page) → verdict thresholds.
- `internal/pipeline/anydoc_test.go` (tag `anydoc`) — fixture PDF/DOCX bytes → markdown, ≥50 chars/page yield check; stub test (no tag) asserts stub error type.
- `internal/pipeline/docling_client_test.go` — httptest server: success shape, 429/503 backoff, breaker opens at 5, non-retryable 4xx.
- `internal/pipeline/chunker_test.go` — parent caps (2048/4096), child window 384/stride 64, breadcrumb rendering, per-window page ranges from line map, neighbour dedupe; golden cases ported from `tests/test_chunking.py` where semantics carry over.
- `internal/pipeline/embedder_test.go` — PlanBatches budget math (port `test_embedder_batches.py`), stitch boundary dedupe (`test_stitch.py`), retry cap counters, idempotent insert.
- `internal/worker/runner_test.go`, `janitor_test.go` — port `test_ack_trim.py`, `test_janitor_*.py`, `test_runner_recycle.py`, `test_retry_ladder.py` semantics: ack+XDEL, PEL retention on failure, recycle exit boundary, reclaim cap → quarantine DLQ row, embed rescue dedup.
- **Acceptance:** `go test ./...` green (unit lanes); `docker compose --profile ingest up` end-to-end: upload a 60-page fixture PDF via the web UI → shards created (~3×20p) → parser logs show anydoc fast-path (<100ms/shard) → children+parents in ParadeDB → doc `ready` with completeness 1.0 → `/v1/search` returns citations with correct page ranges → a forced failure (kill parser mid-shard) recovers via lease reaper without losing the shard.

---

## 4. Phase 4 — Evaluation & rollout (hard cut)

### 4.1 `cmd/lybrix-eval/` — Go eval harness (replaces `scripts/eval`)

Port of `scripts/eval/run.py` + `judge.py` + `compare.py`: loads `scripts/eval/datasets/seed.jsonl` (unchanged golden set), calls the live MCP endpoint (streamable HTTP, bearer key; `initialize`→`tools/call search`), computes hit@top-k / MRR / hit_at_top_k semantics identical to the Python harness (`run.py` flags: `--dataset --top-k --label --junk-filter --collection`), writes results JSON + human summary MD to `scripts/eval/results/` (directory kept), `compare.py` equivalent flags `--baseline --candidate` for pre/post deltas. `scripts/eval/README.md` rewritten for the Go command.

### 4.2 Rollout sequence (hard cut, per user decision)

1. Ship the branch; CI green (Go lanes + web build).
2. `make down-core && make down-ingest` on both hosts; **drop the old Postgres `rag` DB** (corpus is re-ingested; no data migration — hard cut) or use a fresh `rag2` DB and point `DATABASE_URL` at it. Qdrant volume retired after final export sanity check (`curl :6333/collections` snapshot for the record, then discard).
3. `make up-core` (paradedb + lybrix-server serve + web) → `lybrix-server keys bootstrap` → re-create the `API_ADMIN_KEY`/`MCP_API_KEY` values in the web env.
4. `make up-ingest` (lybrix-server workers + docling-serve + tei planes).
5. Re-ingest the Calibre corpus (upload pipeline unchanged; `scripts/backfill` equivalent = `lybrix-eval`'s collection loader or a `lybrix-server admin ingest-dir` convenience subcommand — keep scope minimal: batch `presign/commit` via a small Go loop inside `cmd/lybrix-eval` is enough).
6. Evaluate: run `lybrix-eval` against the golden set; acceptance = hit@8 ≥ 1.0-baseline (53% dense-only / improved with BM25-weight 0.15 per PLAN.md history) and no per-query crash; any regression → tune RRF weights in `internal/store/search.go` SQL (`dense_weight 1.0 / bm25 0.3` per blueprint §5 — note blueprint says 0.3 where 1.0 measured 0.15; start at 0.3, A/B down if eval regresses).
7. Decommission: delete `services/{api,mcp,workers}/`, `libs/`, `migrations/`, `tests/`, root `pyproject.toml`/`uv.lock`/`.python-version`/`.ruff_cache`/`.pytest_cache`, all Python scripts (`scripts/backfill.py`, `scripts/reembed.py`, `scripts/reindex.py`, `scripts/ops_backfill_sparse.py`, and the full `scripts/eval/*.py` harness per §5), old `.env.example` entries; update `CLAUDE.md` (commands, architecture, milestone paragraph), `docs/adr/0003-web-stack.md` addendum, `README.md`. `services/web` stays; `services/web/openapi.json` snapshot stays as the contract artifact.
8. Update `docs/BACKLOG.md`: close rows whose fixes were 1.0-code-specific with a "superseded by 2.0 rewrite" note where the fix must be re-validated; carry forward any still-open rows as Go-port requirements (the implementer must check each open row against this plan).

### 4.3 Phase 4 acceptance

- `go test ./...` fully green including the `anydoc` tag lane on the ingest host.
- Golden-set eval: `hit@8` ≥ baseline, zero endpoint errors, MRR within tolerance of 1.0 numbers.
- Web UI smoke: upload → pipeline page live SSE progress → ready → search works from the playground.
- Janitor drill: `docker kill` a parser mid-book → recovery within one pass; wipe Redis → janitor re-enqueues all non-terminal docs (no blind re-add); poison job → DLQ events row after cap.
- `make lint` (golangci-lint) zero errors; `govulncheck` clean.

---

## 5. Complete file inventory

**Create:** `go.mod`, `go.sum`, `Makefile` (rewrite), `.golangci.yml`, `.github/workflows/ci.yml` (rewrite), `deploy/schema.sql`, `deploy/Dockerfile.lybrix`, `deploy/.env.example`, `cmd/lybrix-server/main.go`, `cmd/lybrix-eval/main.go`, `internal/{config,errors,logging,objectstore,queue,store,api,mcp,pipeline,worker}/…` (files as §1.4/§2/§3), `third_party/anydoc-go/{include/anydoc.h,lib/…,README.md}`, `scripts/build-anydoc-lib.sh`, `internal/**/*_test.go` (~25 files).
**Modify:** `deploy/docker-compose.yml`, `services/web/openapi.json` (only if a field is intentionally renamed — default: no changes), `services/web/README` refs, `CLAUDE.md`, `README.md`, `docs/BACKLOG.md`, `scripts/eval/README.md`, `LYBRIX_2.0_EVOLUTION_PLAN.md` (append "implemented" status notes per phase).
**Replace:** root `PLAN.md` ← this plan (step 0).
**Delete (Phase 4, step 7 only):** `services/api/`, `services/mcp/`, `services/workers/`, `libs/`, `migrations/`, `tests/`, `scripts/{backfill,reembed,reindex,ops_backfill_sparse}.py`, **and the entire Python eval harness `scripts/eval/*.py` — `run.py`, `judge.py`, `compare.py`, `dataset.py`, `filters.py`, `mcp_client.py`, `__init__.py`** (superseded by `cmd/lybrix-eval`; the golden set `scripts/eval/datasets/seed.jsonl`, the results directory `scripts/eval/results/`, and a rewritten `scripts/eval/README.md` stay), `pyproject.toml`, `uv.lock`, `.python-version`, CI python steps. `services/web/` and `docs/` stay. **Zero custom Python remains in the repo after Phase 4** — verify with `find . -name '*.py' -not -path './services/web/node_modules/*'` returning nothing.

## 6. Verification (end-to-end)

1. `go build -tags anydoc ./cmd/lybrix-server` on the ingest host (lib present) and `go build ./...` in CI (stub lane) — both compile.
2. `go test ./...` + `golangci-lint run` — green, per-phase acceptance criteria above.
3. Live E2E: `make up-core && make up-ingest` → upload fixture → ingest completes → `curl -H "Authorization: Bearer $KEY" :8000/v1/search -d '{"query":"…","top_k":8}'` returns citations with parent-backed context; MCP playground `search` + `read_pages` round-trip; eval harness passes golden set.
4. Resilience drills (4.3): kill parser, wipe Redis, poison job.
