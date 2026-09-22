# LYBRIX 2.0 ARCHITECTURE & EVOLUTION BLUEPRINT (REVAMPED)
**Source Document:** Lybrix Architecture Revamp  
**Target Architecture:** WeKnora-Inspired Go Architecture + ParadeDB + docling-serve  
**Status:** Approved Architectural Blueprint

---

## 1. Executive Summary & Goals

Lybrix 2.0 completely modernizes our document ingestion and retrieval stack:
1. **Eliminate Qdrant & Consolidate Storage:** Migrate to **ParadeDB** (PostgreSQL 17 + `pgvector` + `pg_search` BM25). All relational metadata, shards, chunks, BM25 indices, and dense vectors live in a single ACID-compliant database.
2. **Zero Custom Python Services:** Replace the entire Python backend (`services/api`, `services/mcp`, `services/workers`) with:
   - **`lybrix-server` (Go):** A unified high-concurrency binary combining the REST API, Model Context Protocol (MCP) server, async streaming worker orchestration, and janitor routines.
   - **`docling-serve` (Official Docker container):** Offloaded layout analysis and OCR fallback, accessed purely over HTTP REST (`/v1/convert/file`).
   - **AnyDoc (Go):** In-process parser inside `lybrix-server` for born-digital PDFs, DOCX, PPTX, XLSX, and TXT files.
3. **Preserve Sharding Resilience:** A book is never a single atomic job. The 16–24 page shard architecture is retained in Go via Redis Streams, guaranteeing bounded memory, fast retries, and high-throughput parallel parsing.
4. **Hierarchical Parent-Child Retrieval:** Small child chunks (384 tokens) for vector/BM25 matching linked to parent chunks (2048–4096 tokens) or full shard markdown for complete LLM context generation.
5. **Preserve Next.js Web UI:** The existing Next.js 15 App Router web admin console remains untouched, communicating with `lybrix-server` through the standard REST/OpenAPI contract.

---

## 2. System Architecture

```
                                  ┌────────────────────────┐
                                  │      Client / Hermes   │
                                  └───────────┬────────────┘
                                              │ HTTP / MCP (:8430)
                                              ▼
┌────────────────────────────────────────────────────────────────────────────────────────┐
│                                     LYBRIX-SERVER (Go)                                 │
│                                                                                        │
│  ┌───────────────────────┐   ┌───────────────────────────┐   ┌──────────────────────┐  │
│  │   FastMCP Server      │   │     REST API Control      │   │  Janitor / Leases    │  │
│  │  (:8430/mcp HTTP/SSE) │   │        (:8000)            │   │  Health & Reclaim    │  │
│  └───────────┬───────────┘   └─────────────┬─────────────┘   └──────────┬───────────┘  │
│              │                             │                            │              │
│              └─────────────────────────────┼────────────────────────────┘              │
│                                            │                                           │
│  ┌─────────────────────────────────────────┴────────────────────────────────────────┐  │
│  │                            Worker Pipeline Orchestration                         │  │
│  │                                                                                  │  │
│  │   [Splitter]  ───▶  [Parser Gate]  ───▶  [Chunker]  ───▶  [Embedder Pool]        │  │
│  │   (pdfcpu)          ├── AnyDoc (Fast)     (Heading &       (TEI / Ollama Client) │  │
│  │                     └── docling-serve     Parent-Child)                          │  │
│  └──────────────────────────────────────────────────────────────────────────────────┘  │
└──────────────┬───────────────────────────────┬──────────────────────────────┬──────────┘
               │                               │                              │
               ▼                               ▼                              ▼
     ┌───────────────────┐           ┌───────────────────┐          ┌───────────────────┐
     │     ParadeDB      │           │    docling-serve  │          │   TEI / Ollama    │
     │  (Postgres 17 +   │           │    (Container)    │          │  (bge-m3 Embed)   │
     │ pgvector+pg_search│           │ Layout & OCR API  │          └───────────────────┘
     └───────────────────┘           └───────────────────┘
```

---

## 3. Database Schema: Unified ParadeDB (PostgreSQL 17)

All state, metadata, full-text indexes, and embeddings are unified in ParadeDB.

```sql
-- Extensions
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_search;

-- Core Documents
CREATE TABLE documents (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    collection_id UUID NOT NULL,
    title TEXT NOT NULL,
    filename TEXT NOT NULL,
    file_size BIGINT NOT NULL,
    page_count INT NOT NULL,
    mime_type TEXT NOT NULL,
    source_hash VARCHAR(64) NOT NULL,
    state VARCHAR(32) NOT NULL DEFAULT 'pending', -- pending, splitting, parsing, embedding, ready, failed
    error_message TEXT,
    metadata JSONB DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Shards Table (Preserving resilient unit-of-work)
CREATE TABLE shards (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    doc_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    shard_index INT NOT NULL,
    page_start INT NOT NULL,
    page_end INT NOT NULL,
    state VARCHAR(32) NOT NULL DEFAULT 'queued',
    needs_ocr BOOLEAN DEFAULT FALSE,
    mean_chars_per_page FLOAT,
    attempt INT NOT NULL DEFAULT 1,
    lease_expires_at TIMESTAMPTZ,
    parsed_s3_key TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    done_at TIMESTAMPTZ
);

-- Chunks Table with Hierarchical Parent-Child & Hybrid Search
CREATE TABLE chunks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    doc_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    collection_id UUID NOT NULL,
    chunk_hash VARCHAR(64) NOT NULL UNIQUE,
    parent_id UUID REFERENCES chunks(id),
    is_parent BOOLEAN NOT NULL DEFAULT FALSE,
    seq INT NOT NULL,
    page_start INT,
    page_end INT,
    heading_path TEXT[],
    header_breadcrumb TEXT,
    text TEXT NOT NULL,
    token_count INT NOT NULL,
    embedding vector(1024), -- bge-m3 dense vector
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ParadeDB BM25 Search Index (pg_search)
CALL paradedb.create_bm25(
    index_name => 'chunks_bm25_idx',
    schema_name => 'public',
    table_name => 'chunks',
    key_field => 'id',
    text_fields => '{
        "text": {},
        "header_breadcrumb": {}
    }'
);

-- pgvector HNSW Index for Dense Retrieval
CREATE INDEX ON chunks USING hnsw (embedding vector_cosine_ops)
WHERE is_parent = FALSE;

-- API Keys & Usage Tracking
CREATE TABLE api_keys (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    key_hash VARCHAR(64) NOT NULL UNIQUE,
    scopes TEXT[] NOT NULL DEFAULT '{"search"}',
    collections UUID[],
    expires_at TIMESTAMPTZ,
    revoked BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used_at TIMESTAMPTZ
);

CREATE TABLE key_usage (
    id BIGSERIAL PRIMARY KEY,
    api_key_id UUID REFERENCES api_keys(id) ON DELETE CASCADE,
    endpoint TEXT NOT NULL,
    status_code INT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

---

## 4. Parser Strategy: In-Process AnyDoc CGO Binding + docling-serve Fallback

To maximize ingestion speed and eliminate custom Python code, Lybrix 2.0 replicates **Tencent WeKnora's exact CGO pattern**:

### 4.1 WeKnora-Identical In-Process CGO Architecture (`third_party/anydoc-go`)
* **Upstream Crate**: Firecrawl's `anydoc` Rust crate.
* **Build Mechanism (`scripts/build-anydoc-lib.sh`)**:
  - Compiles the Rust crate with Cargo into a static C archive: `libanydoc_go.a`.
  - Links into Go via CGO LDFLAGS:
    ```go
    // #cgo CFLAGS: -I${SRCDIR}/include
    // #cgo linux,amd64 LDFLAGS: -L${SRCDIR}/lib/linux_amd64_gnu -lanydoc_go -lm -lstdc++
    // #include "include/anydoc.h"
    import "C"
    ```
  - Exposes in-process conversion APIs: `anydoc.ToMarkdownBytes(shardBytes, &format)`.
  - Thread safety: Wraps CGO invocations in `runtime.LockOSThread()` / `UnlockOSThread()` to prevent thread-local error register leakage across Go goroutines.
* **Build Tags**: Built with `go build -tags anydoc ./cmd/lybrix-server`.

### 4.2 Two-Tier Fast-Path Execution Flow

```
                 [ 16–24 Page Shard (PDF/Office) ]
                                 │
                                 ▼
                     ┌───────────────────────┐
                     │   In-Process AnyDoc   │
                     │  (CGO / Rust Engine)  │
                     └───────────┬───────────┘
                                 │
                      Inspect Output & Yield
                                 │
           ┌─────────────────────┴─────────────────────┐
           ▼                                           ▼
[ Text Yield >= 50 chars/page ]             [ Yield < 50 chars/page or Err ]
   (Clean Digital Document)                     (Scanned / Complex Layout)
           │                                           │
           ▼                                           ▼
      Fast-Path Complete                     Fallback: HTTP POST
   Output: GitHub-Flavored MD               /v1/convert/file (multipart)
   Duration: 10–50ms per shard              to containerized docling-serve
                                                       │
                                                       ▼
                                            PyTorch OCR & TableFormer
                                            Duration: 15–20s per shard
```

1. **Shard-Level Unit:** A whole 16–24 page shard is passed to `anydoc.ToMarkdownBytes()` in memory. We never split down to per-page requests, avoiding IPC chatter and preserving cross-page headings.
2. **Fast-Path Yield Check:**
   - If `len(markdown) > 0` and `len(markdown) / page_count >= 50 chars`:
     - Accept AnyDoc's markdown output immediately. Shard processing finishes in **sub-100 milliseconds**.
3. **Automated `docling-serve` Fallback:**
   - If `anydoc` returns an error, empty output, or character density `< 50 chars/page` (scanned images/custom fonts):
   - Lybrix sends the entire 16–24 page shard PDF via 1 HTTP multipart POST to `docling-serve` (`http://docling-serve:5001/v1/convert/file`) with `do_ocr=true`.
   - Returns structured Markdown with OCR text and tables.

---

## 5. Chunking & Hybrid Retrieval Engine

### Breadcrumb & Parent-Child Chunking
1. **Parent Chunks:** Full logical sections snapped to headings (`#`, `##`, `###`), capped at 2,048–4,096 tokens. Stored with `is_parent = true` (no dense vector needed).
2. **Child Chunks:** Sliding window within each parent (384 tokens, 64 token overlap).
   - Prefixed with hierarchical breadcrumb: `[Document > Chapter > Section]`
   - Embedded with `bge-m3` (1024-dim vector).
   - Flagged with `is_parent = false` and linked to `parent_id`.

### ParadeDB Hybrid Search Query
ParadeDB combines pgvector cosine similarity and BM25 full-text score with Reciprocal Rank Fusion (RRF) natively in SQL:

```sql
WITH dense_matches AS (
    SELECT id, parent_id, text, heading_path, page_start, page_end,
           ROW_NUMBER() OVER (ORDER BY embedding <=> $1) as dense_rank
    FROM chunks
    WHERE is_parent = FALSE AND collection_id = $2
    ORDER BY embedding <=> $1
    LIMIT $3
),
bm25_matches AS (
    SELECT id, parent_id, text, heading_path, page_start, page_end,
           ROW_NUMBER() OVER (ORDER BY paradedb.score(id) DESC) as bm25_rank
    FROM chunks
    WHERE id @@@ paradedb.parse($4) AND is_parent = FALSE AND collection_id = $2
    LIMIT $3
)
SELECT 
    COALESCE(d.id, b.id) AS chunk_id,
    COALESCE(d.parent_id, b.parent_id) AS parent_id,
    COALESCE(d.text, b.text) AS child_text,
    p.text AS parent_text,
    COALESCE(d.heading_path, b.heading_path) AS heading_path,
    COALESCE(d.page_start, b.page_start) AS page_start,
    COALESCE(d.page_end, b.page_end) AS page_end,
    (COALESCE(1.0 / (60 + d.dense_rank), 0.0) * 1.0 + 
     COALESCE(1.0 / (60 + b.bm25_rank), 0.0) * 0.3) AS rrf_score
FROM dense_matches d
FULL OUTER JOIN bm25_matches b ON d.id = b.id
LEFT JOIN chunks p ON p.id = COALESCE(d.parent_id, b.parent_id)
ORDER BY rrf_score DESC
LIMIT $5;
```

---

## 6. Go Project Structure (`lybrix-server`)

The entire backend is consolidated in Go:

```
lybrix/
├── cmd/
│   └── lybrix-server/          # Main entrypoint: API, MCP, Worker modes
│       └── main.go
├── internal/
│   ├── api/                    # REST API handlers (Chi / Gin)
│   │   ├── middleware.go       # Bearer auth, logging, cors
│   │   ├── documents.go        # Upload, status, doc list
│   │   ├── keys.go             # API key generation & revocation
│   │   └── usage.go            # Key usage summaries
│   ├── mcp/                    # Model Context Protocol server (mcp-go)
│   │   ├── server.go           # FastMCP HTTP/SSE router
│   │   ├── tools_search.go     # Hybrid search tool
│   │   ├── tools_reading.go    # read_pages, get_chunk_context
│   │   └── tools_docs.go       # list_documents, get_document
│   ├── pipeline/               # Ingestion pipeline
│   │   ├── splitter.go         # pdfcpu shard splitter
│   │   ├── gate.go             # Text density & OCR detector
│   │   ├── anydoc.go           # Digital PDF & Office parser
│   │   ├── docling_client.go   # HTTP client for docling-serve
│   │   ├── chunker.go          # Breadcrumb & Parent-Child chunking
│   │   └── embedder.go         # TEI / Ollama HTTP batch client
│   ├── store/                  # ParadeDB store & queries
│   │   ├── db.go               # pgx connection pool
│   │   ├── documents.go
│   │   ├── chunks.go
│   │   ├── search.go           # ParadeDB Hybrid SQL queries
│   │   └── keys.go
│   └── worker/                 # Redis Streams consumer loops & Janitor
│       ├── runner.go           # Stream consumer & lease renewer
│       └── janitor.go          # Stuck job reclaim & health metrics
├── deploy/
│   ├── docker-compose.yml      # ParadeDB, lybrix-server, docling-serve, web, redis
│   └── .env.example
├── services/
│   └── web/                    # Existing Next.js 15 App Router UI
└── go.mod
```

---

## 7. Migration & Rollout Plan

1. **Phase 1: Foundation (ParadeDB + docling-serve Container)**
   - Update `deploy/docker-compose.yml` to spin up `paradedb/paradedb:latest` (PG 17) and `quay.io/docling-project/docling-serve:latest`.
   - Remove Qdrant container and Python worker containers.
   - Run SQL migration scripts initializing tables, BM25 indices, and HNSW indexes.

2. **Phase 2: Core Server (`lybrix-server` in Go)**
   - Implement `store/` with `pgx` and ParadeDB queries.
   - Implement `mcp/` using `mcp-go` (delivering `search`, `get_chunk_context`, `read_pages`, `list_documents`, `get_document`, `list_collections`).
   - Implement `api/` matching the existing OpenAPI spec so Next.js UI connects with zero frontend rewrites.

3. **Phase 3: Pipeline & Parsers**
   - Build `pdfcpu` splitter and AnyDoc fast-path parser in Go.
   - Build `docling-serve` REST client with circuit breakers and retries.
   - Implement breadcrumb parent-child chunker and TEI/Ollama batch embedder.

4. **Phase 4: Fresh In-Place Corpus Re-index**
   - Direct the Calibre collection / PDF storage into the new pipeline.
   - Run parallel ingestion with fast-path parsing.
   - Evaluate hit rates and MRR using the eval harness against the new endpoint.
   - Decommission legacy Python volumes once validated.

---

## Implementation status (2026-09-21, implementer notes)

All four phases implemented on branch `feat/lybrix-2.0-revamp` (uncommitted,
left in the working tree for verification per PLAN.md):

- **Phase 1 — Foundation:** `go.mod` (Go 1.23; pgx/v5, go-redis/v9, chi,
  mcp-go, pdfcpu, pgvector-go, aws-sdk-go-v2, testcontainers+miniredis
  test-only); rewritten `Makefile`/`.golangci.yml`/`.github/workflows/ci.yml`;
  `deploy/Dockerfile.lybrix`; compose rewritten — qdrant/api/mcp/worker-*/
  migrate removed, `paradedb/paradedb:17`, `docling-serve` (ingest profile,
  8g), single `lybrix-server` image with serve on core + splitter/parser×4/
  embedder/janitor on ingest, MinIO kept on the ingest profile; embedded
  `deploy/schema.sql` (collections/documents/shards/chunks with
  parent–child columns + HNSW partial index + guarded
  `paradedb.create_bm25`/events/api_keys(+revoked_at+expires_at)/key_usage/
  metrics_rollup); `internal/{config,errors,logging,queue,objectstore,store}`.
- **Phase 2 — Core server:** `internal/api` (chi, 18 routes matching the
  checked-in OpenAPI snapshot, FastAPI `{"detail"}` envelope, scope table =
  deps.py, commit verify = streaming sha256+`%PDF-`+page-cap, 429
  backpressure, retry scopes incl. R-11 counter unwind, SSE PG-poll stream,
  /pipeline with `chunks_total` replacing `qdrant_points`); `internal/mcp`
  (mcp-go streamable HTTP at /mcp, six tools with verbatim 1.0
  descriptions, bearer 401 parity, /health open, per-method usage rows);
  `cmd/lybrix-server` subcommands serve/splitter/parser/embedder/janitor/
  keys bootstrap/migrate.
- **Phase 3 — Pipeline:** pdfcpu splitter (fixed + chapter-aligned bounds,
  anti-loop guard, degenerate-outline fallback, best-effort bookmarks);
  OCR gate (mean chars/page < 20 via pdfcpu text extraction); anydoc CGO
  binding (`third_party/anydoc-go` header + build script + `LockOSThread`,
  `!anydoc` stub so CI builds clean — linking the real archive happens on
  the ingest host); docling-serve client (jittered backoff, 5-failure
  breaker, fail-fast 4xx); two-tier parser with yield check ≥50 chars/page;
  hierarchical parent–child chunker (384/64 children, 2048/4096 parents,
  breadcrumb prefix at embed time, per-window pages, neighbour dedupe);
  stitch port (boundary-heading dedupe + monotone page map); TEI/Ollama
  client (token-budgeted PlanBatches, jittered backoff, breaker); runner
  (ack+XDEL on commit, PEL retention on failure, recycle-after) and the
  7-step janitor incl. delivery-cap quarantine + metrics rollup.
- **Phase 4 — Eval & hard cut:** `cmd/lybrix-eval` (run + compare over the
  unchanged seed.jsonl, rank metrics + junk filter + collection
  resolution); delete list executed — services/{api,mcp,workers}, libs/,
  migrations/, tests/, Python scripts (backfill/reembed/reindex/
  ops_backfill_sparse + the full scripts/eval/*.py harness),
  pyproject.toml/uv.lock/.python-version/.venv removed; `find . -name
  '*.py'` (excluding web node_modules) returns 0. CLAUDE.md/README.md/
  docs/BACKLOG.md (2.0 disposition)/docs/adr/0003 addendum/scripts/eval/
  README.md updated; services/web untouched, openapi.json snapshot kept.
- **Tests:** config/errors/splitter/chunker/stitch+gate+yield/docling+TEI
  clients/batches unit lanes green; queue lane on miniredis; worker runner
  semantics; API OpenAPI route-parity golden test; store lane via
  testcontainers (skips without a docker socket); eval judge/dataset
  parity incl. seed set. Live E2E + resilience drills are the §4.3
  post-deploy acceptance steps.
