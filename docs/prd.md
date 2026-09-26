<!-- Source: Claude artifact aeb90b43-4859-4a7a-9028-581f8eddd48f (https://claude.ai/public/artifacts/aeb90b43-4859-4a7a-9028-581f8eddd48f), retrieved 2026-09-08 via r.jina.ai. Light formatting normalisation only. -->

## PRD — Document Ingestion & Retrieval Platform

**Status:** Draft v1 · **Owner:** TBD · **Last updated:** 2026-09-08

* * *

## 1. Summary

A self-hosted platform that ingests large PDF books (300–800 pages), converts them to structured text with Docling, chunks and embeds them with a self-hosted embedding server, stores them in a vector database, and exposes the whole corpus for retrieval through an **MCP server** so any MCP-capable client (Claude Desktop, Claude Code, internal agents) can search it.

An **admin web UI** sits on top for upload, monitoring, progress tracking, log inspection, and retry.

Everything runs on Docker Compose on one or more hosts. No Kubernetes.

### 1.1 The central design constraint

Docling has a known, unresolved memory problem on large PDFs: the `docling-parse` C++ backend accumulates memory internally and bypasses the pipeline's own bounded-queue protections, so a 400+ page image-heavy book can OOM even on a 32GB machine. The whole architecture follows from one rule:

> **A book is never a unit of work. A 16–24 page shard is.**

Every scaling, retry, progress, and memory decision in this document is downstream of that.

* * *

## 2. Goals and non-goals

### 2.1 Goals

| # | Goal | Success measure |
| --- | --- | --- |
| G1 | Ingest 400-page PDFs without OOM | Zero OOM-killed workers over a 1,000-book backfill |
| G2 | Book-level wall-clock ingestion under 3 min | p95 book completion ≤ 180s at 8 parser replicas |
| G3 | Full resumability | Any worker/host crash loses ≤ 1 shard of work |
| G4 | Expose corpus via MCP | Claude Desktop can search and cite the corpus with page numbers |
| G5 | Operator self-service | Upload, monitor, diagnose and retry without touching a terminal |
| G6 | Cheap re-embedding | Swapping the embedding model re-runs only embed+index, never re-parses |
| G7 | Query latency isolation | Live query embedding p99 ≤ 80ms while a backfill is running |

### 2.2 Non-goals (v1)

*   Multi-tenant isolation beyond a `collection` label. No per-tenant encryption or row-level security.
*   Real-time / streaming ingestion. This is batch.
*   Generation. The platform retrieves; the MCP client generates.
*   OCR quality tuning beyond "enable when there's no text layer".
*   Kubernetes, service mesh, autoscaling controllers.
*   Document editing or annotation.

### 2.3 Assumptions

Stated explicitly so they're easy to challenge:

*   **Corpus:** ~5,000 books, average 400 pages, mostly born-digital PDFs with a minority of scanned ones. Corpus grows ~100 books/month.
*   **Hardware:** one or two Linux hosts. At least one with an NVIDIA GPU (24GB+) for the embedding server. Parsing runs CPU-only unless a second GPU is available.
*   **Language:** Python 3.12 for everything backend, TypeScript/Next.js for the UI.
*   **Consumers:** 5–50 concurrent MCP clients, mostly interactive (low QPS, latency-sensitive).

* * *

## 3. Personas

| Persona | Needs |
| --- | --- |
| **Librarian / content owner** | Upload books, see what's ingested, spot failures, retry, delete |
| **Platform operator** | Diagnose stuck jobs, read worker logs, scale replicas, watch queue depth |
| **Agent / MCP client** | Fast semantic + keyword search with citations back to page numbers |
| **RAG engineer** | Swap embedding models, tune chunking, A/B retrieval quality |

* * *

## 4. System architecture

### 4.1 Service inventory

| Service | Image base | Role | Stateful | Replicas | Resource profile |
| --- | --- | --- | --- | --- | --- |
| `postgres` | `postgres:16` | Control plane: documents, shards, chunks, jobs, audit log | Yes | 1 | 2 CPU / 4GB |
| `redis` | `redis:7` | Queue broker (Streams) + rate limiting | Yes | 1 | 1 CPU / 1GB |
| `minio` | `minio/minio` | Object storage: source PDFs, parsed JSON, extracted images | Yes | 1 | 1 CPU / 1GB |
| `qdrant` | `qdrant/qdrant` | Vector store | Yes | 1 | 2 CPU / 8GB |
| `tei-ingest` | `ghcr.io/huggingface/text-embeddings-inference:1.9` | Bulk embedding, high batch size | No | 1 | GPU, 12GB VRAM |
| `tei-query` | same | Query embedding, low latency | No | 1 | GPU (shared) or CPU |
| `tei-rerank` | same | Cross-encoder reranking (optional, phase 2) | No | 1 | GPU |
| `api` | app | Control-plane REST API, auth, uploads | No | 2 | 1 CPU / 512MB |
| `worker-splitter` | app | Splits PDFs into page-range shards | No | 2 | 0.5 CPU / 512MB |
| `worker-parser` | app+docling | **The heavy one.** Docling per shard | No | 4–12 | 2 CPU / 8GB each |
| `worker-embedder` | app | Stitch, chunk, call TEI, write vectors | No | 2–4 | 1 CPU / 2GB |
| `worker-janitor` | app | Reaper, retries, TTL cleanup, metrics rollup | No | 1 | 0.25 CPU / 256MB |
| `mcp` | app | MCP server (HTTP + stdio bridge) | No | 2 | 1 CPU / 512MB |
| `web` | node | Next.js admin UI | No | 1 | 0.5 CPU / 512MB |
| `caddy` | `caddy:2` | TLS termination, reverse proxy, basic auth fallback | No | 1 | 0.25 CPU / 256MB |

### 4.2 Pipeline stages

`upload → doc.split → doc.parse (×N shards) → doc.embed → doc.index → ready`

Each arrow is a Redis Stream consumer group. Job state of record lives in Postgres, **not** in Redis. Redis is dispatch only. This is what makes the system resumable: if Redis is wiped, the janitor re-enqueues everything not in a terminal state.

### 4.3 Two planes

*   **Ingest plane** (`worker-*`, `tei-ingest`) — throughput-optimised, tolerant of latency, can saturate hardware.
*   **Query plane** (`mcp`, `tei-query`, `qdrant`) — latency-optimised, must stay responsive during backfills.

They share Qdrant and Postgres but **never** share an embedding server. TEI batches by total token count, so an 8-token query queued behind a 65k-token ingest batch is the single easiest way to destroy p99 latency. Two TEI containers with different `--max-batch-tokens` is the fix.

* * *

## 5. Data model

sql

```
-- Logical grouping for retrieval scoping
CREATE TABLE collections (
  id            TEXT PRIMARY KEY,          -- 'engineering-handbooks'
  name          TEXT NOT NULL,
  embedding_model TEXT NOT NULL,           -- 'BAAI/bge-m3'
  vector_dim    INT  NOT NULL,
  created_at    TIMESTAMPTZ DEFAULT now()
);

CREATE TYPE doc_state AS ENUM (
  'uploaded','splitting','parsing','embedding','indexing',
  'ready','failed','partial','archived'
);

CREATE TABLE documents (
  id              UUID PRIMARY KEY,
  collection_id   TEXT REFERENCES collections(id),
  title           TEXT,
  author          TEXT,
  source_uri      TEXT NOT NULL,           -- s3://raw/{id}.pdf
  content_sha256  TEXT NOT NULL,           -- dedupe key
  page_count      INT,
  byte_size       BIGINT,
  state           doc_state NOT NULL DEFAULT 'uploaded',
  total_shards    INT,
  shards_done     INT NOT NULL DEFAULT 0,
  shards_failed   INT NOT NULL DEFAULT 0,
  chunk_count     INT,
  completeness    NUMERIC(5,4),            -- pages successfully parsed / total
  error_code      TEXT,
  error_detail    TEXT,
  metadata        JSONB DEFAULT '{}',      -- isbn, year, tags, custom fields
  uploaded_by     TEXT,
  created_at      TIMESTAMPTZ DEFAULT now(),
  updated_at      TIMESTAMPTZ DEFAULT now(),
  ready_at        TIMESTAMPTZ,
  UNIQUE (collection_id, content_sha256)
);

CREATE TYPE shard_state AS ENUM ('pending','running','done','failed','skipped');

CREATE TABLE shards (
  doc_id        UUID REFERENCES documents(id) ON DELETE CASCADE,
  idx           INT,
  page_start    INT NOT NULL,              -- inclusive, 1-based
  page_end      INT NOT NULL,              -- inclusive
  state         shard_state NOT NULL DEFAULT 'pending',
  attempts      INT NOT NULL DEFAULT 0,
  needs_ocr     BOOLEAN DEFAULT false,
  parsed_uri    TEXT,                      -- s3://parsed/{doc}/{idx}.json
  worker_id     TEXT,
  lease_until   TIMESTAMPTZ,               -- reaper uses this
  duration_ms   INT,
  peak_rss_mb   INT,
  error_code    TEXT,
  error_detail  TEXT,
  PRIMARY KEY (doc_id, idx)
);

CREATE TABLE chunks (
  id            UUID PRIMARY KEY,
  doc_id        UUID REFERENCES documents(id) ON DELETE CASCADE,
  chunk_hash    TEXT NOT NULL,             -- idempotency key for the vector store
  seq           INT NOT NULL,              -- order within document
  text          TEXT NOT NULL,
  token_count   INT,
  page_start    INT,
  page_end      INT,
  heading_path  TEXT[],                    -- ['Part II','Ch. 7','7.3 Caching']
  embedded_at   TIMESTAMPTZ,
  UNIQUE (doc_id, chunk_hash)
);

-- Append-only operational log, surfaced in the UI
CREATE TABLE events (
  id         BIGSERIAL PRIMARY KEY,
  doc_id     UUID,
  shard_idx  INT,
  level      TEXT,                          -- info | warn | error
  stage      TEXT,                          -- split | parse | embed | index
  code       TEXT,                          -- OOM_SOFT_LIMIT, OCR_FALLBACK, ...
  message    TEXT,
  context    JSONB,
  worker_id  TEXT,
  created_at TIMESTAMPTZ DEFAULT now()
);
CREATE INDEX ON events (doc_id, created_at DESC);
CREATE INDEX ON events (level, created_at DESC);

CREATE TABLE api_keys (
  id          UUID PRIMARY KEY,
  name        TEXT,
  key_hash    TEXT NOT NULL,
  scopes      TEXT[],                       -- ['search','ingest','admin']
  collections TEXT[],                       -- restrict MCP client to a subset
  last_used_at TIMESTAMPTZ,
  revoked_at  TIMESTAMPTZ
);
```

**Design notes.**

*   `content_sha256` unique per collection gives free upload dedupe.
*   `chunk_hash` is the Qdrant point id. Re-running embed is an upsert, never a duplicate.
*   `lease_until` on shards is what lets a janitor reclaim work from a dead worker — cheaper and more reliable than relying on Redis consumer-group claim semantics alone.
*   `completeness` lets you index a book that had 2 bad pages out of 400, flagged rather than dropped.
*   Chunks keep their text in Postgres. Qdrant stores vector + minimal payload. This means you can re-embed without re-parsing, and it keeps the vector store small and fast.

### 4.3 Qdrant collection

python

```
{
  "vectors": {"size": 1024, "distance": "Cosine"},   # bge-m3
  "sparse_vectors": {"bm25": {}},                     # hybrid search
  "payload_schema": {
    "doc_id": "keyword",
    "collection_id": "keyword",
    "page_start": "integer",
    "heading_path": "keyword[]",
    "tags": "keyword[]"
  },
  "hnsw_config": {"m": 16, "ef_construct": 128},
  "quantization_config": {"scalar": {"type": "int8", "always_ram": true}}
}
```

Point id = `chunk_hash`. Payload deliberately does **not** contain the chunk text — the MCP server hydrates text from Postgres by id after the vector search. Keeps the HNSW index in RAM comfortably.

* * *

## 6. Ingestion pipeline specification

### 6.1 Stage 0 — Upload (API)

**Endpoint:**`POST /v1/documents` (multipart) or `POST /v1/documents/presign` → direct-to-MinIO PUT.

For 400-page books at 20–200MB, use presigned direct upload. The API never buffers file bytes.

1.   Client requests presigned URL, gets `{doc_id, upload_url}`.
2.   Client PUTs bytes to MinIO.
3.   Client calls `POST /v1/documents/{id}/commit` with metadata.
4.   API computes sha256 (streaming, from MinIO), checks dedupe, inserts `documents` row in state `uploaded`, XADDs to `doc.split`.

**Backpressure:** if `XLEN doc.parse` exceeds `MAX_PARSE_BACKLOG` (default 2,000), commit returns `429` with `Retry-After`. Accepting 500 books into a queue you can't drain is how you end up with a 3-day backlog and no visibility.

### 6.2 Stage 1 — Splitter

Cheap, CPU-only, never touches Docling.

python

```
SHARD_PAGES = int(env("SHARD_PAGES", 20))

def handle(job):
    path = storage.download(job.source_uri)          # to local tmpfs
    with pikepdf.open(path) as pdf:
        n = len(pdf.pages)
        outline = extract_outline(pdf)               # bookmarks, if any

    bounds = chapter_aligned_bounds(outline, n, SHARD_PAGES) \
             if outline else fixed_bounds(n, SHARD_PAGES)

    with db.tx():
        db.update_doc(job.doc_id, page_count=n,
                      total_shards=len(bounds), state='parsing')
        db.insert_shards(job.doc_id, bounds)

    for i, (s, e) in enumerate(bounds):
        queue.xadd("doc.parse", {"doc_id": job.doc_id, "idx": i,
                                 "page_start": s, "page_end": e,
                                 "source_uri": job.source_uri})
```

**Chapter alignment matters more than it looks.** Snapping shard boundaries to bookmarks eliminates most cross-boundary chunk damage, because chapters rarely split a table or a paragraph. Fall back to fixed 20-page windows with a **1-page overlap** when there's no outline; the embedder dedupes overlapping content by hash.

### 6.3 Stage 2 — Parser (Docling)

This is the service that determines whether the system works. Configuration is not optional.

python

```
from docling.document_converter import DocumentConverter, PdfFormatOption
from docling.datamodel.pipeline_options import PdfPipelineOptions
from docling.datamodel.base_models import InputFormat
from docling.backend.pypdfium2_backend import PyPdfiumDocumentBackend

def build_converter(need_ocr: bool) -> DocumentConverter:
    opts = PdfPipelineOptions()
    opts.do_ocr = need_ocr
    opts.do_table_structure = True
    opts.generate_page_images = False        # largest single memory win
    opts.generate_picture_images = False
    opts.images_scale = 1.0
    return DocumentConverter(format_options={
        InputFormat.PDF: PdfFormatOption(
            pipeline_options=opts,
            backend=PyPdfiumDocumentBackend,  # avoids docling-parse bad_alloc
        )
    })
```

**Per-shard flow:**

1.   Claim shard: `UPDATE shards SET state='running', attempts=attempts+1, worker_id=$w, lease_until=now()+interval '10 minutes' WHERE doc_id=$d AND idx=$i AND state IN ('pending','failed') RETURNING *`. Skip if zero rows (someone else got it).
2.   Download source to `/tmp` (tmpfs, size-limited).
3.   **OCR gate:** extract text for the page range with `pypdfium2`. If mean chars/page < 20, set `needs_ocr=true`. Per-page OCR combined with table detection and cell matching is where memory expands non-linearly, so this stays off for the ~90% of pages that don't need it.
4.   Convert with `page_range=(page_start, page_end)`.
5.   Write `DoclingDocument` JSON to `s3://parsed/{doc_id}/{idx}.json`.
6.   Mark shard done, record `duration_ms` and `peak_rss_mb`, atomically `shards_done = shards_done + 1`.
7.   If `shards_done + shards_failed == total_shards`, XADD to `doc.embed`.

**Memory discipline — all five are required:**

| Control | Setting | Why |
| --- | --- | --- |
| One job per process | `prefetch=1`, `concurrency=1` | Docling RSS is unattributable across threads |
| Process recycling | Fork per job, or `max_tasks_per_child=10` | Native C++ allocations are reclaimed by process exit, not by Python GC |
| Soft RSS budget | Self-check at 6GB, raise `SoftOOM` | Clean retry with a traceback beats an opaque SIGKILL |
| Hard container limit | `mem_limit: 8g` | Backstop only. If this fires you have a bug |
| tmpfs cap | `--tmpfs /tmp:size=2g` | Prevents a pathological PDF filling the host disk |

Set `PARSER_RECYCLE_AFTER=10` as the default. You pay ~3s of model warm-up per recycle; monitor `peak_rss_mb` drift across a worker's lifetime and lower it if RSS climbs.

**Retry ladder** — attempts are not identical:

| Attempt | Change |
| --- | --- |
| 1 | As configured |
| 2 | `SHARD_PAGES / 4` — re-split this shard into 4 sub-shards, requeue |
| 3 | Single-page shards |
| 4 | Disable table structure, text-only extraction |
| Final | Mark shard `failed`, emit event, continue the book |

This is why sharding pays off twice: it bounds memory _and_ it makes failure granular. A poison page kills 1 page out of 400, not the book.

### 6.4 Stage 3 — Embedder

1.   Load all shard JSONs and stitch into one markdown document with a per-line page map (the heading hierarchy is reconciled across shard boundaries, or every chunk after shard 1 loses its section context).
2.   Chunk over the **stitched** doc, not per shard, with the hierarchical chunker in `internal/chunker`: parents ≈2048 runes (context, not embedded) and children ≈384 runes with a 76-rune overlap (embedded). Chunk text is a **verbatim slice of the stitched markdown** — tables, fenced code, lists and paragraph breaks reach the index intact. Regions that must not be split (tables, code fences, LaTeX math, links/images) are protected: a table larger than the budget splits *between rows*, and a table's header row is re-injected into each following chunk so it stays self-describing.
3.   Drop chunks whose hash duplicates any earlier chunk in the doc (handles the 1-page shard overlap and repeated boilerplate).
4.   Insert chunks into Postgres with their page ranges (both parents and children), then embed children in batches of 32–64 texts, max 6 concurrent requests, against `tei-ingest`.
5.   Set doc state `ready` (or `partial` if `shards_failed > 0`), compute `completeness`.

The splitting tier is chosen per document (`CHUNK_STRATEGY=auto` by default): a profiler picks heading-aware, then heuristic, then a plain recursive splitter, and a validator falls through to the next tier when a tier's output looks broken.

A 400-page book yields roughly 1,500–3,000 chunks. Never send them in a single HTTP call; TEI's dynamic batcher handles the packing, your job is to keep a steady stream of moderate requests in flight.

**Re-chunking without re-parsing:** parsed shard markdown is retained in object storage, so a chunker change is applied to the existing corpus with `lybrix-server rechunk` (or `POST /v1/documents/{id}/retry` with `scope=rechunk`), which deletes the doc's chunk rows and re-runs only the stitch→chunk→embed tail. No OCR or conversion work is repeated.

**Client resilience:** exponential backoff on 429/503, circuit breaker after 5 consecutive failures, jittered retry. If TEI is down, the embed job retries later — it must not fail the book and throw away the parse work you already paid for.

### 6.5 Stage 4 — Indexer / finalise

Folded into the embedder for v1; split into its own queue only if you add a reranking-index or a secondary keyword store.

### 6.6 Janitor (continuous)

Runs every 30s:

*   **Reaper:**`shards WHERE state='running' AND lease_until < now()` → back to `pending`. This is how a `docker kill` on a parser recovers.
*   **Requeue:** documents in a non-terminal state with no queue entry (Redis loss, missed XADD).
*   **Escalate:** shards with `attempts >= 4` → `failed` + event.
*   **Stuck detector:** documents in the same state for >`STUCK_MINUTES` → warn event, surfaced as a UI banner.
*   **TTL:** delete parsed JSON for archived documents; roll up events older than 30 days.

### 6.7 Error taxonomy

Error codes are a product surface, not an implementation detail — they drive the UI's retry affordances.

| Code | Stage | Retryable | UI treatment |
| --- | --- | --- | --- |
| `PDF_ENCRYPTED` | split | No | "Password required" — prompt for upload replacement |
| `PDF_CORRUPT` | split | No | Terminal, offer delete |
| `SHARD_OOM` | parse | Yes, degraded | Auto retry ladder, show attempt count |
| `SHARD_TIMEOUT` | parse | Yes | Auto |
| `OCR_FAILED` | parse | Yes, text-only | Auto, flags reduced quality |
| `EMBED_UNAVAILABLE` | embed | Yes | Auto, banner: "Embedding service down" |
| `EMBED_DIM_MISMATCH` | embed | No | Config error — collection model vs. TEI model |
| `VECTOR_UPSERT_FAILED` | index | Yes | Auto |
| `DEDUPE_CONFLICT` | upload | No | Link to the existing document |

* * *

## 7. Retrieval and the MCP server

### 7.1 Retrieval strategy

1.   **Hybrid search** — dense (bge-m3) + sparse BM25 in Qdrant, fused with RRF. Purely dense retrieval on technical books loses on exact terms: version numbers, API names, acronyms.
2.   **Filter** by `collection_id`, plus optional `doc_id`, `tags`, `page_start` range.
3.   **Rerank** (phase 2) — top 50 → cross-encoder via `tei-rerank` → top 8.
4.   **Context expansion** — optionally fetch the ±1 neighbouring chunks by `(doc_id, seq)` so the client gets coherent passages rather than fragments.
5.   **Hydrate** text from Postgres by chunk id.

### 7.2 MCP server

Transport: streamable HTTP (`/mcp`) behind Caddy, plus a stdio shim for local Claude Desktop. Auth: `Authorization: Bearer <api_key>`; the key's `collections` array scopes every tool call server-side.

**Tools exposed:**

| Tool | Input | Output | Notes |
| --- | --- | --- | --- |
| `search` | `query`, `collection?`, `top_k=8`, `filters?` | chunks with text, `doc_title`, `page_start–page_end`, `heading_path`, score | The primary tool |
| `list_collections` | — | id, name, doc count, model | Lets the agent scope |
| `list_documents` | `collection?`, `query?`, `state?`, `limit` | id, title, author, pages, state, completeness | Browse/verify |
| `get_document` | `doc_id` | metadata, TOC/heading tree, chunk count | Orientation before deep read |
| `read_pages` | `doc_id`, `page_start`, `page_end` | markdown for that range | Bounded to 30 pages/call |
| `get_chunk_context` | `chunk_id`, `window=2` | neighbouring chunks in order | Follow-up after a search hit |

**Design rules for the tool surface:**

*   Every result carries a citation triple `(doc_title, page_start, page_end)`. An answer the user can't verify against a page number is worth much less.
*   Bound every response. `read_pages` caps at 30 pages; `search` caps `top_k` at 25. An MCP tool that can return 400 pages will blow the client's context and produce worse answers than one that returns 8 good chunks.
*   Tool descriptions state _when_ to use each one, not just what it does — that's what drives correct model behaviour.
*   `search` returns `partial: true` and a note when a matched document has `completeness < 1.0`, so the agent knows the corpus has a hole there.

### 7.3 Query path latency budget

| Step | Budget |
| --- | --- |
| Auth + parse | 5ms |
| Query embedding (`tei-query`) | 25ms |
| Qdrant hybrid search | 30ms |
| Postgres hydrate | 10ms |
| Rerank (phase 2) | 60ms |
| **Total p99** | **≤ 150ms** with rerank, ≤ 80ms without |

* * *

## 8. Admin UI specification

Next.js app, server components against the control-plane API. Auth via session cookie (OIDC if you have an IdP, otherwise username/password in Postgres with argon2).

### 8.1 Screens

**1. Dashboard**

*   Four counters: documents ready / in-flight / failed / partial.
*   Queue depth sparkline per stage (`doc.split`, `doc.parse`, `doc.embed`).
*   Throughput: pages/min, chunks/min, over 1h/24h/7d.
*   Worker health strip: parser replicas with current shard, RSS, uptime.
*   Active alert banners: embedding service down, backlog over threshold, N documents stuck.

**2. Documents (list)**

*   Table: title, collection, pages, state, progress bar, completeness, uploaded by, updated at.
*   Filters: state, collection, tag, date range, "has failures".
*   Full-text search on title/author/metadata.
*   Bulk select → retry, reprocess, archive, delete, move collection.
*   Live updates via SSE on `/v1/events/stream` — a progress bar that requires a refresh is not a progress bar.

**3. Document detail**

*   Header: metadata, state, completeness, timings per stage.
*   **Shard grid** — the most useful widget in the product. One cell per shard, coloured by state, tooltip showing page range, attempts, duration, peak RSS. A book with a red cell at shard 14 tells the operator exactly where to look in one glance.
*   Click a shard → its events, its error, "retry this shard" button.
*   Tabs: Chunks (paginated preview with page refs), Events (filterable log), Raw (links to source PDF and parsed JSON in MinIO).
*   Actions: retry failed shards, re-embed (skips parsing), reprocess from scratch, archive, delete.

**4. Upload**

*   Drag-and-drop, multi-file, presigned direct-to-MinIO with per-file progress.
*   Collection picker, tags, optional metadata (title/author/year — prefilled from PDF metadata, editable).
*   Duplicate detection shown before commit: "This file matches an existing document."
*   Optional: folder upload with a CSV manifest for bulk metadata.

**5. Logs**

*   Unified event stream across documents, filterable by level, stage, error code, document, worker, time range.
*   Deep-link from any document or shard.
*   Raw worker output stays in `docker compose logs`; the UI links out with the exact command, pre-filled with service and time window, for the rare case where the event log isn't enough.

**6. Collections**

*   CRUD. Embedding model and dimension are set at creation and immutable — changing a model means creating a new collection and running a re-embed migration.
*   Per-collection: doc count, chunk count, vector count, index size.

**7. Settings**

*   API keys: create, scope (`search` / `ingest` / `admin`), restrict to collections, revoke. Show the raw key exactly once.
*   Pipeline config: shard size, OCR threshold, retry ladder, backlog cap, worker recycle interval.
*   MCP connection snippet, ready to paste into a client config.

### 8.2 Progress semantics

Book-level progress must be honest, and shard-level progress makes it easy:

```
progress = (shards_done + shards_failed) / total_shards * 0.75   # parse is 75%
         + embed_progress * 0.25
```

Show the phase name alongside the bar ("Parsing 14/20 shards"), and always show elapsed time. During `splitting` the total is unknown — show an indeterminate bar, not a fake percentage.

### 8.3 Retry semantics in the UI

Three distinct buttons, because they cost very different amounts:

| Action | Re-runs | Typical cost |
| --- | --- | --- |
| **Retry failed shards** | Only shards in `failed` | Seconds |
| **Re-embed** | Chunk + embed + index, reuses parsed JSON | ~30s |
| **Reprocess** | Everything from the source PDF | Minutes |

Never offer only "retry". An operator who has to re-parse 5,000 books because the embedding model changed will not forgive the UI.

* * *

## 9. Control-plane API

```
POST   /v1/documents/presign          → {doc_id, upload_url}
POST   /v1/documents/{id}/commit      → 202 | 409 duplicate | 429 backlog
GET    /v1/documents                  ?state=&collection=&q=&cursor=
GET    /v1/documents/{id}
GET    /v1/documents/{id}/shards
GET    /v1/documents/{id}/chunks      ?cursor=
GET    /v1/documents/{id}/events
POST   /v1/documents/{id}/retry       {scope: shards|embed|full}
DELETE /v1/documents/{id}             ?purge_vectors=true
POST   /v1/documents/bulk             {ids[], action}

GET    /v1/collections
POST   /v1/collections
GET    /v1/collections/{id}/stats

POST   /v1/search                     {query, collection, top_k, filters}
GET    /v1/events/stream              (SSE, for live UI updates)

GET    /v1/system/health              per-dependency
GET    /v1/system/queues              depth + consumer lag per stream
GET    /v1/system/workers             live worker registry
GET    /v1/system/metrics             JSON counters + histograms (in-process)
```

Idempotency: `Idempotency-Key` header honoured on all POSTs.

* * *

## 10. Observability

No separate metrics or log-aggregation stack. Postgres is already the source of truth for job state, so it also carries the operational telemetry, and the admin UI is the only dashboard. This removes four containers and roughly 5GB of RAM; the cost is no long-horizon time-series and no ad-hoc log querying across services.

### 10.1 Where the data lives

| Signal | Store | Surfaced in |
| --- | --- | --- |
| Per-shard duration, `peak_rss_mb`, attempts, error code | `shards` table | Document detail, shard grid |
| Stage transitions, warnings, tracebacks | `events` table | Logs screen, document detail |
| Queue depth and consumer lag | Redis, read live | Dashboard, `/v1/system/queues` |
| Throughput and latency counters | `metrics_rollup` table, written by the janitor every 60s | Dashboard sparklines |
| Raw container output | `docker compose logs` | Not in the UI — link-out only |

The janitor writes one rollup row per minute:

sql

```
CREATE TABLE metrics_rollup (
  bucket        TIMESTAMPTZ PRIMARY KEY,   -- minute granularity
  pages_parsed  INT,
  shards_done   INT,
  shards_failed INT,
  chunks_embedded INT,
  parse_p50_ms  INT,
  parse_p95_ms  INT,
  peak_rss_p95_mb INT,
  queue_depth   JSONB,                     -- {"doc.parse": 412, ...}
  search_p95_ms INT,
  search_count  INT
);
```

Retain at minute granularity for 7 days, roll up to hourly for 90 days, then drop. That's a few MB and it covers every question you'll actually ask ("was last night's backfill slower than the previous one?", "when did RSS start climbing?").

### 10.2 Conditions worth alerting on

The janitor evaluates these on each pass and writes an `events` row plus, optionally, a webhook POST (Slack/Discord/ntfy — one env var, no service).

| Condition | Trigger | Surfaced as |
| --- | --- | --- |
| Parse backlog growing | `doc.parse` depth rising for 15 min | Dashboard banner |
| Shard OOM rate | `SHARD_OOM`> 5% of attempts over 30 min | Banner + webhook |
| RSS creep | p95 `peak_rss_mb`> 5,500 | Banner + webhook |
| Embedding unavailable | `tei-ingest` health failing 2 min | Banner + webhook |
| Stuck documents | any doc non-terminal > 60 min | Banner, links to the docs |
| Query latency | p95 search > 300ms over 5 min | Banner |

### 10.3 Logging

Structured JSON to stdout, with `doc_id`, `shard_idx`, `worker_id` and `stage` on every line. Docker's json-file driver with rotation is the only collector:

yaml

```
x-logging: &logging
  logging:
    driver: json-file
    options: {max-size: "50m", max-file: "5"}
```

Anything an operator needs to see goes into the `events` table explicitly via `write_event()` — a traceback, a degraded-retry notice, an OCR fallback. Treat stdout as debug output for when you're already SSH'd in, and the `events` table as the product surface. If you don't enforce that split, the useful signal ends up only in container logs where the UI can't reach it.

### 10.4 If you outgrow this

The trigger is either "I need to correlate across services" or "I need more than 90 days". At that point add a single `docker-compose.obs.yml` overlay with Prometheus and Grafana. Keep the app's counters exposed at `/v1/system/metrics` in a shape that's trivially convertible to Prometheus text format so the migration is a serializer swap, not a rewrite.

* * *

## 11. Security

*   Caddy terminates TLS, single ingress. Only `caddy` publishes ports; every other service is on internal networks.
*   Three networks: `edge` (caddy, api, web, mcp), `internal` (api, workers, mcp, postgres, redis, qdrant, minio), `gpu` (embedder, mcp, tei-*). Parsers do not need Qdrant access; don't give it to them.
*   API keys hashed with argon2; scopes enforced per request; `collections` array scopes MCP clients to a subset of the corpus.
*   MinIO with per-service credentials: parsers get read on `raw` + write on `parsed`; the API gets presign rights only.
*   Uploads: magic-byte validation, size cap, filename sanitisation, and page-count cap (reject 5,000-page PDFs at the door rather than discovering them in a parser).
*   Audit log on all mutating admin actions.

* * *

## 12. Repository layout

Python monorepo with a shared library, plus the Next.js app. Every deployable has its own Dockerfile; workers share one image with different entrypoints (one image build, four commands — much faster CI than four images).

```
lybrix/
├── README.md
├── Makefile                       # up, down, logs, migrate, seed, test, scale
├── .env.example
├── pyproject.toml                 # uv workspace root
├── uv.lock
│
├── libs/
│   ├── core/                      # shared domain — no service imports this backwards
│   │   └── src/core/
│   │       ├── config.py          # pydantic-settings, single source of env truth
│   │       ├── db/
│   │       │   ├── models.py      # SQLAlchemy
│   │       │   ├── repo.py        # document/shard/chunk repositories
│   │       │   └── session.py
│   │       ├── queue/
│   │       │   ├── streams.py     # Redis Streams producer/consumer group
│   │       │   └── contracts.py   # pydantic job payloads, versioned
│   │       ├── storage/s3.py      # MinIO client, presign
│   │       ├── observability/
│   │       │   ├── logging.py     # structlog JSON
│   │       │   ├── metrics.py
│   │       │   └── tracing.py
│   │       ├── errors.py          # the error taxonomy from §6.7
│   │       └── events.py          # write_event() helper
│   │
│   ├── parsing/                   # everything Docling
│   │   └── src/parsing/
│   │       ├── converter.py       # build_converter(), pinned options
│   │       ├── splitter.py        # page-range + chapter-aligned bounds
│   │       ├── ocr_gate.py        # text-layer detection
│   │       ├── stitch.py          # shard JSON → single DoclingDocument
│   │       └── memory.py          # RSS guard, SoftOOM
│   │
│   ├── chunking/
│   │   └── src/chunking/
│   │       ├── hybrid.py          # HybridChunker wrapper
│   │       ├── dedupe.py          # overlap removal
│   │       └── headings.py        # heading_path reconstruction
│   │
│   ├── embedding/
│   │   └── src/embedding/
│   │       ├── client.py          # TEI client: batching, retry, circuit breaker
│   │       └── models.py          # model registry: dim, tokenizer, max_len
│   │
│   └── retrieval/
│       └── src/retrieval/
│           ├── qdrant.py          # collection mgmt, upsert, hybrid query
│           ├── search.py          # RRF fusion, filters, hydration
│           └── rerank.py
│
├── services/
│   ├── api/
│   │   ├── Dockerfile
│   │   └── src/api/
│   │       ├── main.py
│   │       ├── deps.py            # auth, db session, rate limit
│   │       ├── routers/
│   │       │   ├── documents.py
│   │       │   ├── collections.py
│   │       │   ├── search.py
│   │       │   ├── events.py      # SSE stream
│   │       │   └── system.py
│   │       └── schemas/
│   │
│   ├── workers/
│   │   ├── Dockerfile             # heavy: torch, docling models baked in
│   │   └── src/workers/
│   │       ├── runner.py          # generic consumer loop: claim, lease, ack, DLQ
│   │       ├── splitter.py
│   │       ├── parser.py          # the §6.3 worker
│   │       ├── embedder.py
│   │       └── janitor.py
│   │
│   ├── mcp/
│   │   ├── Dockerfile
│   │   └── src/mcp_server/
│   │       ├── server.py          # FastMCP, HTTP transport
│   │       ├── tools/
│   │       │   ├── search.py
│   │       │   ├── documents.py
│   │       │   └── pages.py
│   │       └── auth.py            # bearer key → scopes + collections
│   │
│   └── web/
│       ├── Dockerfile
│       ├── package.json
│       ├── app/
│       │   ├── (dashboard)/page.tsx
│       │   ├── documents/page.tsx
│       │   ├── documents/[id]/page.tsx
│       │   ├── upload/page.tsx
│       │   ├── logs/page.tsx
│       │   ├── collections/page.tsx
│       │   └── settings/page.tsx
│       ├── components/
│       │   ├── shard-grid.tsx     # the §8.1 widget
│       │   ├── progress-bar.tsx
│       │   ├── event-log.tsx
│       │   └── upload-dropzone.tsx
│       └── lib/api-client.ts
│
├── migrations/                    # alembic
│   └── versions/
│
├── deploy/
│   ├── docker-compose.yml         # base: all services
│   ├── docker-compose.dev.yml     # overrides: hot reload, exposed ports, no GPU
│   ├── docker-compose.gpu.yml     # overrides: TEI on GPU
│   ├── caddy/Caddyfile
│   └── minio/init.sh              # bucket creation + policies
│
├── scripts/
│   ├── backfill.py                # bulk ingest a directory
│   ├── reembed.py                 # migrate a collection to a new model
│   ├── reindex.py                 # rebuild Qdrant from Postgres chunks
│   └── health.sh
│
├── tests/
│   ├── unit/
│   ├── integration/               # testcontainers: pg, redis, qdrant, minio
│   └── fixtures/pdfs/             # small, scanned, table-heavy, encrypted, corrupt
│
└── docs/
    ├── prd.md                     # this file
    ├── runbook.md                 # what to do when X alerts
    └── adr/                       # 0001-shard-size.md, 0002-hybrid-search.md, ...
```

* * *

## 13. Deployment (Docker Compose)

### 13.1 Base file

yaml

```
# deploy/docker-compose.yml
name: lybrix

x-app-env: &app-env
  DATABASE_URL: postgresql+psycopg://rag:${POSTGRES_PASSWORD}@postgres:5432/rag
  REDIS_URL: redis://redis:6379/0
  S3_ENDPOINT: http://minio:9000
  S3_ACCESS_KEY: ${MINIO_ROOT_USER}
  S3_SECRET_KEY: ${MINIO_ROOT_PASSWORD}
  QDRANT_URL: http://qdrant:6333
  TEI_INGEST_URL: http://tei-ingest:80
  TEI_QUERY_URL: http://tei-query:80
  LOG_LEVEL: ${LOG_LEVEL:-info}

x-restart: &restart
  restart: unless-stopped

services:
  postgres:
    <<: *restart
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: rag
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
      POSTGRES_DB: rag
    volumes: [pgdata:/var/lib/postgresql/data]
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U rag"]
      interval: 10s
      retries: 5
    networks: [internal]

  redis:
    <<: *restart
    image: redis:7-alpine
    command: redis-server --appendonly yes --maxmemory 768mb --maxmemory-policy noeviction
    volumes: [redisdata:/data]
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 10s
    networks: [internal]

  minio:
    <<: *restart
    image: minio/minio
    command: server /data --console-address ":9001"
    environment:
      MINIO_ROOT_USER: ${MINIO_ROOT_USER}
      MINIO_ROOT_PASSWORD: ${MINIO_ROOT_PASSWORD}
    volumes: [miniodata:/data]
    healthcheck:
      test: ["CMD", "mc", "ready", "local"]
      interval: 15s
    networks: [internal]

  qdrant:
    <<: *restart
    image: qdrant/qdrant:latest
    volumes: [qdrantdata:/qdrant/storage]
    environment:
      QDRANT__SERVICE__API_KEY: ${QDRANT_API_KEY}
    networks: [internal, gpu]

  tei-ingest:
    <<: *restart
    image: ghcr.io/huggingface/text-embeddings-inference:1.9
    command: >
      --model-id ${EMBED_MODEL:-BAAI/bge-m3}
      --max-batch-tokens 65536
      --max-concurrent-requests 512
      --dtype float16
    volumes: [hfcache:/data]
    networks: [gpu]

  tei-query:
    <<: *restart
    image: ghcr.io/huggingface/text-embeddings-inference:1.9
    command: >
      --model-id ${EMBED_MODEL:-BAAI/bge-m3}
      --max-batch-tokens 8192
      --max-concurrent-requests 64
      --dtype float16
    volumes: [hfcache:/data]
    networks: [gpu]

  api:
    <<: *restart
    build: {context: .., dockerfile: services/api/Dockerfile}
    environment: {<<: *app-env}
    depends_on:
      postgres: {condition: service_healthy}
      redis: {condition: service_healthy}
    healthcheck:
      test: ["CMD", "curl", "-fs", "http://localhost:8000/v1/system/health"]
      interval: 15s
    networks: [edge, internal, gpu]

  worker-splitter:
    <<: *restart
    build: {context: .., dockerfile: services/workers/Dockerfile}
    command: ["python", "-m", "workers.splitter"]
    environment: {<<: *app-env, WORKER_PREFETCH: "1"}
    mem_limit: 1g
    tmpfs: ["/tmp:size=2g"]
    depends_on: [postgres, redis, minio]
    networks: [internal]

  worker-parser:
    <<: *restart
    build: {context: .., dockerfile: services/workers/Dockerfile}
    command: ["python", "-m", "workers.parser"]
    environment:
      <<: *app-env
      WORKER_PREFETCH: "1"
      WORKER_CONCURRENCY: "1"
      PARSER_RECYCLE_AFTER: "10"
      PARSER_SOFT_RSS_MB: "6144"
      SHARD_LEASE_SECONDS: "600"
      OMP_NUM_THREADS: "2"          # stops OpenMP from grabbing every core
    mem_limit: 8g
    cpus: 2.0
    tmpfs: ["/tmp:size=2g"]
    depends_on: [postgres, redis, minio]
    networks: [internal]

  worker-embedder:
    <<: *restart
    build: {context: .., dockerfile: services/workers/Dockerfile}
    command: ["python", "-m", "workers.embedder"]
    environment: {<<: *app-env, EMBED_BATCH_SIZE: "48", EMBED_CONCURRENCY: "6"}
    mem_limit: 3g
    depends_on: [postgres, redis, minio, qdrant]
    networks: [internal, gpu]

  worker-janitor:
    <<: *restart
    build: {context: .., dockerfile: services/workers/Dockerfile}
    command: ["python", "-m", "workers.janitor"]
    environment: {<<: *app-env, JANITOR_INTERVAL: "30"}
    mem_limit: 512m
    networks: [internal]

  mcp:
    <<: *restart
    build: {context: .., dockerfile: services/mcp/Dockerfile}
    environment: {<<: *app-env}
    depends_on: [postgres, qdrant, tei-query]
    networks: [edge, internal, gpu]

  web:
    <<: *restart
    build: {context: .., dockerfile: services/web/Dockerfile}
    environment:
      API_URL: http://api:8000
      NEXT_PUBLIC_APP_URL: ${PUBLIC_URL}
    depends_on: [api]
    networks: [edge]

  caddy:
    <<: *restart
    image: caddy:2-alpine
    ports: ["80:80", "443:443"]
    volumes:
      - ./caddy/Caddyfile:/etc/caddy/Caddyfile:ro
      - caddydata:/data
    depends_on: [api, web, mcp]
    networks: [edge]

  migrate:
    build: {context: .., dockerfile: services/api/Dockerfile}
    command: ["alembic", "upgrade", "head"]
    environment: {<<: *app-env}
    depends_on:
      postgres: {condition: service_healthy}
    restart: "no"
    networks: [internal]

volumes: {pgdata: {}, redisdata: {}, miniodata: {}, qdrantdata: {}, hfcache: {}, caddydata: {}}
networks:
  edge: {}
  internal: {internal: true}
  gpu: {internal: true}
```

### 13.2 GPU overlay

yaml

```
# deploy/docker-compose.gpu.yml
services:
  tei-ingest:
    deploy:
      resources:
        reservations:
          devices: [{driver: nvidia, count: 1, capabilities: [gpu]}]
  tei-query:
    deploy:
      resources:
        reservations:
          devices: [{driver: nvidia, device_ids: ["0"], capabilities: [gpu]}]
```

Both TEI containers can share one GPU — bge-m3 at fp16 is ~2.5GB, so two instances plus KV overhead fits comfortably in 24GB. If you have no GPU, swap the image tag to the CPU variant and expect roughly 10× lower throughput; a 5,000-book backfill becomes a weekend job rather than an afternoon.

### 13.3 Dev overlay

yaml

```
# deploy/docker-compose.dev.yml
services:
  api:
    command: ["uvicorn", "api.main:app", "--host", "0.0.0.0", "--reload"]
    volumes: ["../services/api/src:/app/src", "../libs:/libs"]
    ports: ["8000:8000"]
  worker-parser:
    volumes: ["../services/workers/src:/app/src", "../libs:/libs"]
    mem_limit: 4g
  web:
    command: ["npm", "run", "dev"]
    volumes: ["../services/web:/app", "/app/node_modules"]
    ports: ["3000:3000"]
  postgres: {ports: ["5432:5432"]}
  redis:    {ports: ["6379:6379"]}
  minio:    {ports: ["9000:9000", "9001:9001"]}
  qdrant:   {ports: ["6333:6333"]}
```

### 13.4 Operating it

bash

```
# first boot
cp .env.example .env && $EDITOR .env
docker compose -f docker-compose.yml -f docker-compose.gpu.yml run --rm migrate
docker compose -f docker-compose.yml -f docker-compose.gpu.yml up -d

# scale the parser pool — this is your throughput dial
docker compose up -d --scale worker-parser=10

# drain before maintenance: stop consuming, let in-flight shards finish
docker compose stop worker-splitter
docker compose exec api python -m scripts.wait_drain
docker compose stop worker-parser
```

Put these behind Makefile targets (`make up`, `make scale N=10`, `make drain`) so nobody has to remember the overlay ordering.

**Scaling rule of thumb:** set `worker-parser` replicas to `min(floor(RAM_GB / 8), floor(CORES / 2))`. On a 64GB / 32-core host that's 8 parsers, which is 8 × 8GB = 64GB worst case — so use 6 and leave headroom for Postgres, Qdrant and the page cache. Compose won't stop you from over-committing; the kernel OOM killer will.

**Compose-specific caveats to design around:**

*   `--scale` gives no rolling deploys. Deploying a new parser image means `up -d --no-deps worker-parser`, which restarts all replicas at once. The lease/reaper mechanism in §6.6 is what makes this safe — in-flight shards get reclaimed within 30s.
*   No built-in autoscaling. Either run the pool at peak size and accept idle cost, or add a small janitor task that shells out to `docker compose up -d --scale` when `doc.parse` depth crosses a threshold. Start with fixed sizing; add the script only if backlog patterns are genuinely bursty.
*   Everything is one host by default. If you outgrow it, the natural split is parsers on a second host with Postgres/Redis/MinIO reachable over the LAN — no code changes, just a different `.env`.

### 13.5 Backup

*   `postgres` — nightly `pg_dump` to MinIO, plus WAL archiving if the corpus is expensive to rebuild.
*   `minio` — `mc mirror` to offsite. The `raw/` bucket is the only truly irreplaceable data; `parsed/` can be regenerated, and Qdrant can be rebuilt entirely from Postgres via `scripts/reindex.py`.
*   `qdrant` — snapshot API weekly. Treat it as a cache, not a source of truth.

* * *

## 14. Capacity planning

| Quantity | Estimate |
| --- | --- |
| Shard parse time (20 pages, born-digital, tables on) | 10–30s |
| Shard parse time (20 pages, scanned + OCR) | 60–180s |
| Book (400pp) on 1 parser | 5–10 min |
| Book (400pp) on 8 parsers | 60–90s wall clock |
| Chunks per book | 1,500–3,000 |
| Embedding tokens per book | ~600k |
| Embed time per book (`tei-ingest`, A100-class) | 10–20s |
| Vector storage, 5,000 books @ int8 | ~12GB |
| Postgres, 5,000 books with chunk text | ~40GB |
| MinIO, raw + parsed | ~500GB |
| **5,000-book backfill, 8 parsers** | **~4–5 days** born-digital; longer with OCR |

The backfill number is the one to sanity-check against your hardware before committing. If it's unacceptable, the levers in order of effectiveness: more parser replicas (linear), disable table structure detection (~40% faster, quality cost), a second host.

* * *

## 15. Milestones

| Phase | Scope | Exit criteria |
| --- | --- | --- |
| **M1 — Spine** (wk 1–2) | Compose stack, schema, split→parse→embed→index, CLI ingest | One 400-page book ingests end to end with no OOM |
| **M2 — Durability** (wk 3) | Retry ladder, reaper, leases, error taxonomy, DLQ | Kill a parser mid-book; it completes anyway |
| **M3 — Retrieval + MCP** (wk 4–5) | Hybrid search, MCP server, auth, tool surface | Claude Desktop searches the corpus and cites page numbers |
| **M4 — Admin UI** (wk 6–8) | Dashboard, list, detail + shard grid, upload, logs, retry | Operator runs a 100-book backfill without a terminal |
| **M5 — Operability** (wk 9) | Health checks, in-app metrics + alert banners, webhook notifier, runbook | All §10 conditions fire correctly in a game day |
| **M6 — Scale** (wk 10–12) | Full backfill, reranking, re-embed migration tooling | 5,000 books ready; p99 search under budget during backfill |

* * *

## 16. Open questions

1.   **Shard size.** 20 pages is a starting default. Benchmark 10/20/30 on your actual corpus and pick on the memory-vs-overhead curve. Record the result as `docs/adr/0001-shard-size.md`.
2.   **Embedding model.** bge-m3 assumed (multilingual, 1024-dim, sparse+dense in one model). If the corpus is English-only and technical, a smaller model may retrieve just as well at 3× throughput.
3.   **Cross-page tables.** Docling doesn't natively reconstruct tables spanning page boundaries, and sharding makes it slightly worse. Options: accept it, post-process merge on heuristics, or over-fetch shard boundaries by 2 pages. Needs a decision before M3.
4.   **Chunk text location.** Kept in Postgres here. If chunk volume grows past ~50M rows, consider moving text to MinIO with Postgres holding only pointers.
5.   **Multi-tenancy.** Collections give logical separation but not isolation. If real tenants arrive, this needs revisiting before, not after.
6.   **Docling version pinning.** Pin exactly and test upgrades against the fixture corpus — memory behaviour has changed materially between releases and there are open issues in this area.
