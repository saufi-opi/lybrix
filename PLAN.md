# Dynamic Model Registry + Multi-Source Ingestion (WeKnora-inspired)

**Role:** planner · **Branch:** `feat/dynamic-model-providers` · **Date:** 2026-09-22
**Revision:** 5 — resolves the five revision-4 verifier issues (rebind-vs-non-goal terminology, streamed ingestion memory bounds, dial-time SSRF enforcement, explicit EPUB pipeline branching, unconditional old-index removal). Keeps every fix from revisions 2–3. Reply sentinel: `PLAN_READY`.

---

## 1. Objective

Two workstreams on top of the 2.0 Go core:

**A. Model registry.** Eliminate hardcoded embedding settings (`EMBED_BACKEND`, `EMBED_MODEL`, `TEI_INGEST_URL`, `TEI_QUERY_URL`, `EMBED_BATCH_SIZE`, `EMBED_CTX_BUDGET`, `EMBED_TRUNCATE_CHARS`, `EMBED_QUERY_PREFIX`) in favor of a **database-backed, UI-manageable model registry**. Models are bound **per collection — explicitly and mandatorily** at creation; ingest resolves the model through the document's collection.

**B. Multi-source ingestion.** `/upload` becomes a tabbed hub: **Local Files** (multi-file drag-and-drop, presigned direct-to-MinIO), **Remote URL** (server-side streamed fetch → MinIO → enqueue), **OPDS Connector** (Calibre-Web native: browse any OPDS feed with Basic Auth, sync selected/all books into a collection).

**User-locked decisions:**
1. **Dimensions: fully arbitrary** (1..2000, operator-set in the UI). `chunks.embedding` is **untyped `vector`**; per-dimension partial HNSW indexes created on demand (`EnsureDimIndex`).
2. **Binding: per collection, MANDATORY at creation.** `POST /v1/collections` requires `embedding_model_id`; the collection **inherits** the model's `model_id` + `vector_dim`. The seeded default model row still exists (legacy-collection compat + cross-collection query plane), but is never silently substituted at collection creation. **Rebinding an existing collection is permitted** via `POST /v1/collections/{id}/model` (metadata sync); there is **no automated background re-embed migration runner in v1** — after a rebind, stale-dim vectors are excluded from dense search until a re-embed lands (manual re-embed endpoints or a future runner).
3. **Providers v1:** `tei` + `ollama` + **OpenAI-compatible** (`/v1/embeddings`, per-model API key, write-only).
4. **Env retirement: hard cut + seed** — legacy env names are read **only** as first-boot seed values when the registry table is empty.
5. **No connector credential storage** — OPDS URL/username/password are per-session in the UI; nothing persisted server-side.

**Non-goals (precise):** **no automated background re-embed migration runner in v1** (manual re-embed via existing per-doc endpoints; stale-dim vectors excluded from dense search after a rebind), no reranker registry, no OPDS auto-sync scheduling, no per-doc model override.

---

## 2. Scope

**In:** `embedding_models` table + store layer + `EnsureDimIndex`; untyped vector column + **unconditional old-index removal**; 4 registry REST paths; **3 ingestion REST paths** (`fetch-url`, `opds/browse`, `opds/sync`) over a **disk-streaming** `ingestFromStream` helper with **dial-time SSRF enforcement**; mandatory collection binding (+ Collections create dialog in the UI); rebind endpoint; OpenAI provider branch; dim validation (422 at save, client guard at embed/query, 503 backstop); **explicit EPUB pipeline branch** (mime gate → single synthetic shard → docling-only → non-splitting retry ladder); health/pipeline re-pointed at the registry; seed-on-boot with index provisioning; tabbed `/upload` hub; snapshot 18 → **25** paths; env/compose cleanup; tests.

**Out:** re-embed migration runner, rerank registry, connector persistence/scheduling, per-doc model override.

---

## 3. Schema Edits

Both copies stay byte-identical: `internal/store/schema.sql` and `deploy/schema.sql`. Applied idempotently by the advisory-lock bootstrap (`internal/store/db.go:49`).

```sql
-- New table (append after api_keys block):
CREATE TABLE IF NOT EXISTS embedding_models (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL UNIQUE,              -- display name, e.g. "bge-m3 @ ingest host"
    provider TEXT NOT NULL CHECK (provider IN ('tei','ollama','openai')),
    model_id TEXT NOT NULL,                 -- 'BAAI/bge-m3', 'nomic-embed-text', 'text-embedding-3-small'
    ingest_url TEXT NOT NULL,               -- ingest-plane endpoint (two-plane rule §4.3)
    query_url TEXT NOT NULL,                -- query-plane endpoint
    api_key TEXT,                           -- openai provider only; write-only, never serialized out
    vector_dim INT NOT NULL CHECK (vector_dim > 0 AND vector_dim <= 2000),  -- arbitrary; 2000 = pgvector HNSW cap
    query_prefix TEXT NOT NULL DEFAULT 'search_query: ',
    batch_size INT NOT NULL DEFAULT 48 CHECK (batch_size >= 1),
    ctx_budget INT NOT NULL DEFAULT 1900 CHECK (ctx_budget >= 1),
    truncate_chars INT NOT NULL DEFAULT 6000 CHECK (truncate_chars >= 0),
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_embedding_models_single_default
    ON embedding_models ((is_default)) WHERE is_default;

-- Collections binding (idempotent). MANDATORY at creation from now on; the
-- column stays nullable so pre-registry collections keep resolving via the
-- seeded default row (read path only — see §5 resolution rules).
ALTER TABLE collections ADD COLUMN IF NOT EXISTS embedding_model_id UUID REFERENCES embedding_models(id);
-- legacy embedding_model / vector_dim columns remain (OpenAPI CollectionOut
-- compat) and are kept in sync FROM the bound row on create/bind.

-- chunks.embedding: untyped (no typmod) so any dimension stores cleanly.
CREATE TABLE IF NOT EXISTS chunks (
    ...
    embedding vector,                       -- was vector(1024)
    ...
);

-- REMOVAL (verifier issue 5): the old bare index DDL
--   CREATE INDEX IF NOT EXISTS ix_chunks_hnsw ON chunks
--       USING hnsw (embedding vector_cosine_ops) WHERE is_parent = FALSE;   -- schema.sql:98
-- is DELETED from both schema copies, not superseded. It cannot survive the
-- untyped column: pgvector refuses to build a bare HNSW on a dimension-less
-- column ("column does not have dimensions" → schema apply fails at boot on
-- fresh databases), and on mixed-dim data it would reject non-1024 inserts.

-- Migration DO block (runs every boot). Uses catalog views pg_attribute /
-- pg_class (information_schema.columns lacks atttypid/atttypmod — rev-3
-- issue 1). Two unconditional clauses: strip the typmod when present, and
-- drop the legacy bare index whenever it exists — including on databases
-- whose column already converged but which still carry ix_chunks_hnsw.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_attribute a
        JOIN pg_class c ON c.oid = a.attrelid
        WHERE c.relname = 'chunks' AND a.attname = 'embedding'
          AND format_type(a.atttypid, a.atttypmod) LIKE 'vector(%)'
    ) THEN
        ALTER TABLE chunks ALTER COLUMN embedding TYPE vector;
    END IF;

    IF EXISTS (SELECT 1 FROM pg_class WHERE relname = 'ix_chunks_hnsw') THEN
        DROP INDEX ix_chunks_hnsw;
    END IF;
END $$;

-- Default-dimension HNSW (1024, the seed dim): partial expression index — the
-- predicate stops maintenance of other-dimension rows (a bare cast would
-- ERROR inserts of mismatched dims).
CREATE INDEX IF NOT EXISTS ix_chunks_hnsw_1024 ON chunks
    USING hnsw ((embedding::vector(1024)) vector_cosine_ops)
    WHERE is_parent = FALSE AND vector_dims(embedding) = 1024;
```

**Per-dimension index strategy:** one partial expression HNSW per distinct dim (`ix_chunks_hnsw_<dim>`, predicate `vector_dims(embedding) = <dim>`), created on demand by `EnsureDimIndex(ctx, dim)` — invoked from model Insert/Update/Bind, **the seed path**, and collection create. Plain `CREATE INDEX` under the advisory lock. The dense CTE must match the index expression (literal dim) + predicate or the planner won't use it. pgvector HNSW caps at 2000 dims; the CHECK keeps every model indexable.

---

## 4. Exact Edits Per File

### Backend — registry store

1. **`internal/store/schema.sql` + `deploy/schema.sql`** — DDL above (both copies identical): new table, `ALTER collections`, untyped `embedding vector`, **deletion of the `ix_chunks_hnsw` DDL block (schema.sql:98–99 + deploy copy)**, migration DO block (pg_attribute/pg_class predicate + unconditional legacy-index drop), `ix_chunks_hnsw_1024` partial expression index.
2. **`internal/store/models.go`** — add `EmbeddingModel` struct (`ID, Name, Provider, ModelID, IngestURL, QueryURL, HasAPIKey bool, VectorDim, QueryPrefix, BatchSize, CtxBudget, TruncateChars, IsDefault, CreatedAt, UpdatedAt`). Never expose `api_key`.
3. **`internal/store/embeddingmodels.go`** (new) — pgx methods in the `collections.go` style:
   `ListEmbeddingModels` (`is_default DESC, created_at`), `GetEmbeddingModel`, `GetDefaultEmbeddingModel`, `InsertEmbeddingModel` (clears previous default; calls `EnsureDimIndex`), `UpdateEmbeddingModel` (api_key omitted → unchanged; dim change → `EnsureDimIndex`), `DeleteEmbeddingModel` (409 when collection-referenced or default), `BindCollectionModel` (sets `embedding_model_id`, syncs legacy columns from the row, `EnsureDimIndex`), `CountCollectionRefs`, `SeedDefaultEmbeddingModel` (idempotent `INSERT … WHERE NOT EXISTS`; **calls `EnsureDimIndex(seed dim)`**), `EnsureDimIndex(ctx, dim)` (advisory lock, checks `pg_indexes`, creates `ix_chunks_hnsw_<dim>` when absent), `ResolveCollectionModel(ctx, collectionID)` (one SQL: collections ⨝ embedding_models; NULL/unbound collection → default row; empty registry → nil).

### Backend — config hard cut

4. **`internal/config/config.go`** — **Remove** `TEIIngestURL`, `TEIQueryURL`, `EmbedBackend`, `EmbedModel`, `EmbedDim`, `EmbedBatchSize`, `EmbedCtxBudget`, `EmbedTruncateChars`, `EmbedQueryPrefix` (fields at lines 33–44, `fromEnv` reads 216–227, `Validate` branches 264–274). **Add** seed block (`EmbedSeed{Provider←EMBED_BACKEND "tei", ModelID←EMBED_MODEL "BAAI/bge-m3", Dim←EMBED_DIM 1024, IngestURL←TEI_INGEST_URL "http://localhost:8081", QueryURL←TEI_QUERY_URL "http://localhost:8082"}`). **Keep** `EmbedConcurrency/MaxAttempts/RetryWindowSec` + `TEIRerankURL`. `Validate` drops the backend check.
5. **`internal/config/config_test.go`** — removed fields no longer parsed; seed vars read; backend/batch validation cases move to api tests.

### Backend — embed client generalization

6. **`internal/pipeline/tei_client.go`**
   - `EmbedSpec{Provider, ModelID, IngestURL, QueryURL, APIKey, TruncateChars}`; `NewEmbedClient(spec, plane)` replaces `NewTeiClient` (URL picked by plane; breaker/backoff untouched).
   - `openai` branch: `POST {base}/v1/embeddings`, `{"model", "input"}`, `Authorization: Bearer <api_key>`, response `{"data":[{"embedding":[…]}]}` normalized in the decode switch; `{"error":{"message"}}` → `TeiUnavailable`, fast-fail on 401/404.
   - **Dim guard:** `Embed(ctx, texts, expectedDim int)` (0 = unchecked); length mismatch → non-retryable `DimMismatchError{Expected, Actual}`. Ingest and query callers always pass the model's declared dim.
   - **`Probe(ctx) (reachable bool, latencyMS int, detectedDim int, detail string)`**: provider health (tei `/health`, ollama `/api/tags`, openai `GET /v1/models`; 2s), then one 1-token embed of `"ping"` for the true output dim. Powers `/v1/models/test` and save-time dim confirmation.
7. **`internal/pipeline/tei_client_test.go`** (new) — openai shapes, plane URL selection, dim guard match/mismatch, probe parsing (no docker).

### Backend — resolution & consumers

8. **`internal/service/embedder.go`** (line ~96): resolve `deps.DB.ResolveCollectionModel(ctx, docCollection(doc))` — nil row → `errors.NewPlatformError(errors.CodeEmbedDimMismatch, "no embedding model configured")`; build `pipeline.NewEmbedClient(spec, "ingest")`; `PlanBatches(texts, tok, m.CtxBudget, m.BatchSize)` (line 111); every `tei.Embed` passes `expectedDim: m.VectorDim` (mismatch → `CodeEmbedDimMismatch`, existing entry `internal/errors/errors.go:31`).
9. **`cmd/lybrix-server/helpers.go`** — `embedQueryFn(db)`; signature `func(ctx, collection, text string) (vec []float32, model *store.EmbeddingModel, err error)`: resolve model, apply `m.QueryPrefix` inside, plane `"query"`, return the model.
10. **`internal/api/server.go`** / **`internal/mcp/server.go`** — `Deps.EmbedQuery` per item 9. Callers: `internal/api/search.go:63`, `internal/mcp/tools.go:62` (pass collection, drop prefix concat, forward `model.VectorDim`).
11. **`internal/store/search.go`** — `HybridSearch(…, dim int, …)`; dense CTE filters `vector_dims(embedding) = <dim>` (literal, matches the index predicate) and orders by `embedding::vector(<dim>) <=> $1` (literal cast, matches the index expression). Other-dimension rows excluded, never error. Backstop: Postgres dimension-mismatch error (`different vector dimensions`) → typed store error → 503 `detail` naming the model.
12. **`internal/api/system.go`** — `handleHealth` keeps the `healthPayload` shape; `TeiQuery` = provider-aware probe of the **default model's** `query_url` (no row → `"down"`). `handlePipeline`: `components["tei_query"]`/`components["embed_backend"]` probe the default model's query/ingest URLs; add `embedding_model` + `embed_provider` keys. `checkEmbedBackend` (line 233) → `checkEmbedModel(ctx, *store.EmbeddingModel) string`.
13. **`cmd/lybrix-server/main.go`** — seed hook inside `infra` for every subcommand: `store.SeedDefaultEmbeddingModel(ctx, db, settings.EmbedSeed)`.

### Backend — REST: registry paths

14. **`internal/api/models.go`** (new) — handlers in the `keys.go` style (`writeDetail`, 422 validation, audit via `WriteEventPool`):
    - `modelOut{id, name, provider, model_id, ingest_url, query_url, has_api_key, vector_dim, query_prefix, batch_size, ctx_budget, truncate_chars, is_default, created_at, updated_at}` (never `api_key`).
    - Validation: name 1–200, provider ∈ {tei,ollama,openai}, `model_id` required, URLs parse as http(s), `vector_dim` int 1–2000 else 422, openai requires api_key on create.
    - **Dim confirmation on create/update:** when `Probe` succeeds and `detectedDim > 0` ≠ declared → 422 `"declared vector_dim (%d) does not match the model's actual output dimension (%d); confirm with POST /v1/models/test"`. Probe unreachable → proceed (runtime `expectedDim` guard catches it later).
15. **`internal/api/server.go`** — routes:
    ```go
    r.Get("/v1/models", s.handleListModels)
    r.Post("/v1/models", s.handleCreateModel)
    r.Post("/v1/models/test", s.handleTestModel)        // dry-run probe, no persist
    r.Post("/v1/models/{model_id}", s.handleUpdateModel)
    r.Delete("/v1/models/{model_id}", s.handleDeleteModel)
    r.Post("/v1/collections/{collection_id}/model", s.handleBindCollectionModel)
    ```
16. **`internal/api/middleware.go`** `routeScope` (line 120) — the six registry paths → `"admin"`; the three ingestion paths (item 18) → `"ingest"`.

### Backend — REST: collections (mandatory binding + rebind)

17. **`internal/api/collections.go`** — **`handleCreateCollection` rework (BREAKING for the create contract):**
    - `embedding_model_id` **required**: absent → 422 `"field required: embedding_model_id"`; unknown → 404 `"embedding model not found"`. The legacy literals `collections.go:58–62` are **deleted**.
    - Legacy `embedding_model`/`vector_dim` columns filled **from the bound model row** (inheritance); `EnsureDimIndex` runs for the dim.
    - `collectionOut` gains `embedding_model_id *string`.
    - `handleBindCollectionModel` **rebinds an existing collection** (metadata sync only; no vector rewrite in v1 — dense search excludes stale-dim rows via the `vector_dims(embedding) = <dim>` filter). The re-embed itself runs through the existing per-doc re-embed endpoints; there is no automated migration runner (Non-goal).

### Backend — REST: multi-source ingestion (3 new paths)

18. **`internal/api/ingest.go`** (new) — shared internal helper + three handlers, reusing `handleCommit`'s semantics (`internal/api/documents.go:136-212`): backlog 429 gate, `FindDuplicate` 409, `InsertDocument`, `queue.XAddJob(StreamSplit, SplitJob{SchemaVersion, DocID, SourceURI})`, 202 `{"id","state"}`:
    - **`ingestFromStream(ctx, r io.Reader, collectionID, filename, mimeType string, title, author *string, metadata map[string]any) (docID string, httpStatus int, detail string)`** — **disk-streaming, not RAM-buffered (verifier issue 2):** the body goes to a temp file (`os.MkdirTemp` + `os.CreateTemp` pattern — `splitter.go:65-69` already uses it), digested with `sha256` via `io.TeeReader` while streaming to disk. Then:
      - **PDF:** sniff `%PDF-` on the first bytes; page-cap early stop stays **during** the download — when bytes exceed `MaxDocumentPages*40KB` (~32 MB at the default 800-page cap) abort with `errTooLarge` before the write completes. After the download, `pdfPageCount(tempFile)` (pdfcpu takes a path, so no full-buffer needed) + over-cap check.
      - **EPUB:** sniff `PK\x03\x04`; hard ceiling **64 MiB** (generous for any ebook; enforced by byte accounting during streaming, aborting mid-download) — the 512 MiB figure from rev 4 is dropped entirely since the API must never hold large bodies.
      - On success: `PutObject` (or multipart upload for files > 32 MiB via the existing `manager.Uploader`) from the temp file into MinIO at `objectstore.RawKey(docID)`, temp file removed on all paths. Returns typed outcomes mapping to commit-parity 400/409/422/429 details (`errObjectNotPDF`-class, sha mismatch, over-cap).
    - **`POST /v1/documents/fetch-url`** — body `{collection_id, url, title?, author?, metadata?}`. **Dial-time SSRF enforcement (verifier issue 3):** a dedicated `http.Client` with a custom `http.Transport` whose **`DialContext` resolves the host and validates EVERY returned IP** against private/loopback/link-local/multicast ranges (RFC1918, 127/8, 169.254/16, ::1, fc00::/7, fe80::/10, 224.0.0.0/4) **before dialing**, plus a `Control` hook re-checking the actual connected address — this defeats DNS rebinding because the check runs at connect time on every connection, not resolve-then-connect. One shared client serves the whole redirect chain: `CheckRedirect` (≤10 hops) keeps using the same transport, so every hop is dial-checked too; only http(s) schemes allowed; 10s dial / 10min total timeout; per-response byte accounting enforces the PDF/EPUB caps above. Body → `ingestFromStream`. Success → 202; failure → 400/409/429 with commit-parity details.
    - **`POST /v1/connectors/opds/browse`** — body `{url, username, password, feed_url?}` (credentials not persisted). GET with Basic Auth on `feed_url ?? url` (same SSRF-guarded client); parse OPDS Atom XML (title, authors, navigation links, acquisition links with mime type). 200 `{entries: [{title, authors, summary, acquisition: [{href, mime_type}]}], next_href?}`. Upstream 401 → 400 `"OPDS feed rejected credentials"`; parse failure → 400 `"not an OPDS/Atom feed"`.
    - **`POST /v1/connectors/opds/sync`** — body `{url, username, password, collection_id, selection?: [{title, href, mime_type}]}`; `selection` omitted → every acquisition entry on the given feed page. Per entry: GET the acquisition href (Basic Auth, SSRF-guarded client) → stream into `ingestFromStream` (title/author from the OPDS entry). Sequential, per-item context timeout; 200 `{synced, failed, results: [{title, status: "accepted"|"duplicate"|"rejected", doc_id?, detail?}]}`. One feed page per call — the `next_href` loop lives in the UI.
19. **EPUB pipeline branching (verifier issue 4 — explicit contract per stage):**
    - **Verify/ingest time (`ingestFromStream`, item 18):** magic-byte branch writes `documents.mime_type = 'application/epub+zip'` for EPUB (the column default is `'application/pdf'`, `schema.sql:31` — EPUB docs must NOT inherit it); `page_count` stored as **1**; `content_sha256` computed the same streamed way.
    - **Splitter (`internal/service/splitter.go:55-95`):** branch on `doc.MimeType`. For `'application/epub+zip'`: **skip** `pipeline.PageCount` (pdfcpu would raise `CodePDFCorrupt`), **skip** `ExtractBookmarks`/`ChapterAlignedBounds`, download to the temp dir as `source.epub`, and insert exactly **one synthetic shard** — `idx=0, page_start=1, page_end=1, state='pending'` — then enqueue the `ParseJob` and mark the doc `parsing`, identical to the tail of the PDF path.
    - **Parser/two-tier (`internal/pipeline/parser.go`):** EPUB shards **skip the anydoc fast path entirely** and go straight to docling-serve (it parses EPUB natively); the shard's markdown is treated as one page (`page_start=1, page_end=1`), so the stitch/page-map path needs no changes. The PDF cache path (`PARSER_PDF_CACHE_DIR`) is bypassed for EPUB.
    - **Retry ladder (`internal/service/parser.go:21-26`):** for EPUB shards the ladder is **single-shard re-parse without page-halving** — attempt 2's quarter-split branch (and attempt 3's single-page split) are no-ops for a `page_start=1,page_end=1` shard, so retries re-attempt the same single shard (docling retry with backoff) until `EmbedMaxAttempts`-style escalation marks the shard failed; no duplicate sub-shards are ever created.
    - Local dropzone + `fetch-url` + OPDS sync all accept `application/pdf` and `application/epub+zip`; any other mime → 400 `"unsupported file type"` at sniff time.
20. **`internal/api/openapi_snapshot.json` + `services/web/openapi.json`** — hand-extend the snapshot (embedded + served byte-identical by `internal/api/openapi.go`): registry ops (item 15) + `fetch-url` + `opds/browse` + `opds/sync`, with `ModelOut/ModelCreate/ModelUpdate/ModelTestResult/FetchUrlRequest/OpdsBrowseRequest/OpdsBrowseResult/OpdsSyncRequest/OpdsSyncResult` schemas and the `CollectionCreate` change. Path count 18 → **25**.
21. **`internal/api/api_test.go`** — parity count `18 → 25` (line 26) + header comment (line 14); `TestRouteScopeTable` gains six `"admin"` + three `"ingest"` rows; new handler unit tests (below).
22. **`internal/errors/errors.go`** — no new codes; ingestion failures surface as 4xx `detail` (commit parity).

### Frontend (services/web)

23. **Regenerate client**: `curl http://localhost:8000/openapi.json > services/web/openapi.json && cd services/web && npm run generate-client`.
24. **`services/web/lib/queries.ts`** — add `useEmbeddingModels()`; mutations via the keys-manager `adminFetch` pattern.
25. **`services/web/app/(dashboard)/layout.tsx`** — NAV entry `{ href: "/models", label: "Models" }` (before Settings).
26. **`services/web/app/(dashboard)/models/page.tsx`** (new) → `<ModelsManager />`, `dynamic = "force-dynamic"`.
27. **`services/web/components/models-manager.tsx`** (new) — mirror `keys-manager.tsx` (sonner, shadcn Dialog/Card/Table/Badge/Select/Input/Label, `/api/admin/v1/*` only): table (name, provider badge, model_id, URLs, editable `vector_dim`, default badge, status dot); create/edit dialog (provider select, api_key password field write-only/openai-only, **`vector_dim` numeric input with preset chips 384/768/1024/1536/3072 + free entry**, query_prefix, batch_size, ctx_budget, truncate_chars, set-default checkbox; dim-confirmation 422 surfaces both dims); row actions Test / Edit / Delete (409 surfaces bound-count or default reason) / Set default. `prefers-reduced-motion` respected; no new deps.
28. **`services/web/app/(dashboard)/collections/page.tsx`** — **"New collection" dialog**: name + id inputs and an **embedding-model dropdown fed by `GET /v1/models`** (mandatory — submit disabled until a model is chosen; shows the model's dim). Table gains bound model + inherited dim columns; the stale CardDescription becomes "Each collection is bound to a registry model at creation and inherits its dimension — manage models on the Models page; rebinding is available per collection."
29. **`services/web/app/(dashboard)/upload/page.tsx`** — **tabbed multi-source hub** (shadcn `Tabs`; collection picker is a dropdown of existing collections):
    - **Tab 1 — Local files:** dropzone (`onDrop`-style handlers, dashed border + drag highlight, `application/pdf` + `application/epub+zip`, multiple) preserving the existing flow — parallel per-file client-side SHA-256 (`crypto.subtle`), presigned PUT, itemized per-file progress + inline log + toasts (`sha256Hex` and the presign/commit sequence at `upload/page.tsx:11-77` carry over, now concurrent with a small limiter).
    - **Tab 2 — Remote URL:** one URL or newline-separated batch, optional title override, sequential `POST /api/admin/v1/documents/fetch-url` per URL with per-item status lines (accepted/duplicate/rejected + detail).
    - **Tab 3 — OPDS:** feed URL + username + password (session-only), "Browse" → `opds/browse`; selectable catalog table (checkbox, title/author/formats), "Sync selection" / "Sync all on this page" → `opds/sync` per page with a results table; "Next page" follows `next_href`.
30. **`services/web/app/api/admin/[...path]/route.ts`** — add a `DELETE` export mirroring `POST` (model deletion); path-forwarding already covers the new paths.

### Deploy / docs

31. **`deploy/docker-compose.yml`** — remove `EMBED_BACKEND`, `EMBED_MODEL`, `EMBED_BATCH_SIZE`, `EMBED_CTX_BUDGET`, `EMBED_TRUNCATE_CHARS`, `EMBED_QUERY_PREFIX` from `x-core-env`/`x-ingest-env`; keep `TEI_INGEST_URL`/`TEI_QUERY_URL` (seed) and `TEI_MODEL` (TEI args, lines 295/318).
32. **`deploy/.env.example`** — annotate seed vars (incl. `EMBED_DIM`) as "first-boot seed only — manage models in the UI afterwards".
33. **`CLAUDE.md`** — status note: DB-backed model registry (arbitrary per-model dims, per-dim HNSW), mandatory per-collection binding + rebind endpoint, multi-source ingestion (presign / fetch-url / OPDS), EPUB single-shard path; embed env vars are seed-only.

---

## 5. REST API Contracts

All `Bearer`-scoped, FastAPI `{"detail": …}` envelope, snapshot-pinned.

| Method & Path | Scope | Request | Responses |
|---|---|---|---|
| `GET /v1/models` | admin | — | 200 `ModelOut[]` |
| `POST /v1/models` | admin | `ModelCreate` | 201 · 422 (`vector_dim 1..2000`, unknown provider, **declared≠detected dim when probe succeeds**) · 409 duplicate name |
| `POST /v1/models/test` | admin | `ModelCreate` (api_key optional) | 200 `{reachable, latency_ms\|null, detail, vector_dim\|null}` — never persists |
| `POST /v1/models/{model_id}` | admin | `ModelUpdate` (partial; api_key null = unchanged) | 200 · 404 · 422 · 409 name clash |
| `DELETE /v1/models/{model_id}` | admin | — | 204 · 404 · 409 `"model bound to N collection(s)"` · 409 `"cannot delete the default model"` |
| `POST /v1/collections/{collection_id}/model` | admin | `{embedding_model_id}` | 200 `collectionOut` (dim inherited) · 404 |
| `POST /v1/documents/fetch-url` | ingest | `{collection_id, url, title?, author?, metadata?}` | 202 `{id, state}` · 400 unreachable/not-PDF-or-EPUB/too-large/SSRF-blocked · 409 duplicate · 429 backlog `Retry-After` |
| `POST /v1/connectors/opds/browse` | ingest | `{url, username, password, feed_url?}` | 200 `{entries[], next_href?}` · 400 bad credentials / not Atom |
| `POST /v1/connectors/opds/sync` | ingest | `{url, username, password, collection_id, selection?[]}` | 200 `{synced, failed, results[]}` · 404 collection · 400 bad credentials |
| `POST /v1/collections` (changed) | admin | `CollectionCreate` — **`embedding_model_id` required** | 201 · 422 missing field · 404 unknown model · 409 id exists |

`ModelOut`: `{id, name, provider, model_id, ingest_url, query_url, has_api_key, vector_dim, query_prefix, batch_size, ctx_budget, truncate_chars, is_default, created_at, updated_at}`.

**Runtime resolution rules:**
- Collection creation: explicit model only — no default-model substitution (decision 2). Legacy collections (`embedding_model_id NULL`) keep resolving via the seeded default row on the read path. Rebinding an existing collection is permitted (metadata sync); no automated re-embed migration runner exists in v1 — stale-dim vectors are excluded from dense search until a re-embed runs.
- Embed job: doc → collection → bound model (legacy NULL → default row; empty registry → `EMBED_DIM_MISMATCH` platform error, janitor keeps retrying until a model exists). Client `expectedDim` guard rejects mismatched vectors before any DB write.
- Query (`/v1/search`, MCP `search`): explicit collection → its bound model (legacy NULL → default); no collection → default model. Dense CTE filters `vector_dims(embedding) = <model dim>`; other-dim rows excluded, never error; Postgres dim-mismatch backstop → 503 naming the model.
- New-dimension first use (create/bind/seed) provisions `ix_chunks_hnsw_<dim>` automatically.

---

## 6. UI Specification

**`/models`** (§4 item 27): keys-page patterns, dark-only paper & press tokens, serif `text-[19px]` titles, `bg-paper-deep` code chips; `vector_dim` numeric field with preset chips; API key `<Input type="password">` "stored server-side, never displayed again"; touch targets ≥44px; mobile single-column.

**`/upload` hub** (§4 item 29): `Tabs` (Local files / Remote URL / OPDS). Tab 1 dropzone is a large dashed-border region (drag state highlights border + bg), multi-file, PDF+EPUB, parallel hashing/uploads with per-file progress rows. Tab 2/3 reuse the same per-item status-line + toast idiom. Collection pickers are dropdowns everywhere (no free-text collection id). No persisted OPDS credentials anywhere.

---

## 7. Acceptance Criteria

1. `go test ./...` passes clean (store lane may skip without docker); new pipeline/api/config tests all run. `make lint`, `make vet` pass — the standing quality gates (§8 step 13).
2. Snapshot parity: `internal/api/openapi_snapshot.json` == `services/web/openapi.json` byte-identical; parity test asserts **25** paths (comment updated); `npm run generate-client` clean; `npm run build` + `npm run lint` pass.
3. Seed: empty registry + seed env → exactly one default model after any subcommand boot (idempotent) **with the seed dim's HNSW index present** (`EMBED_DIM=768` boot → `ix_chunks_hnsw_768`).
4. Arbitrary dims: 768 model registers → index on bind; doc embeds 768-dim vectors; `HybridSearch` returns hits via the 768 index. `vector_dim` 0/negative/>2000 → 422.
5. Dim safety: declared≠actual → 422 at save when the probe succeeds; client `expectedDim` guard rejects mismatched embed output before any DB write; no Postgres 500 path remains (503 backstop).
6. Registry CRUD: 409s (duplicate name, delete-bound, delete-default), default uniqueness, `api_key` never in any response/log/event.
7. `POST /v1/collections` without `embedding_model_id` → **422**; with it → legacy columns inherit the model's model/dim; unknown id → 404. `POST /v1/collections/{id}/model` rebinds (metadata sync); after a rebind the dense search excludes stale-dim rows and returns hits from current-dim rows only.
8. Local upload: multi-file drag-drop of PDFs (+EPUB) end-to-end (hash → presign → PUT → commit → ready), parallel with per-file progress.
9. `fetch-url`: an arxiv-style PDF URL lands ready (202 → split → parse → embed); duplicate content → 409; unreachable host → 400; a private-address URL (e.g. `http://169.254.169.254/`) → 400 **at dial time** (the guard also holds across redirects — a redirect to a private address is rejected); oversize → 400 mid-download, never buffering a full 512 MiB body in API RAM (disk-streamed; PDF early stop at `MaxDocumentPages*40KB`, EPUB ceiling 64 MiB).
10. OPDS: browse a Basic-Auth OPDS feed returns parsed entries with acquisition links; sync of a selected page ingests each book (accepted/duplicate/rejected per item); wrong credentials → 400; credentials never persisted or logged.
11. EPUB end-to-end: an EPUB upload stores `mime_type='application/epub+zip'`, `page_count=1`; the splitter creates exactly one synthetic shard (no pdfcpu call, no bookmarks); the parser routes to docling-serve (anydoc skipped); a forced failure re-attempts the same single shard without spawning sub-shards; the doc reaches `ready` and its chunks are searchable.
12. Health/pipeline probe the default model's URLs from the registry; no `Settings.TEI*` reads remain in `system.go`; payload shapes client-compatible.
13. Removed env vars no longer read (`config_test.go` proves it); compose app env cleaned; TEI containers still boot from `TEI_MODEL`.
14. UI: `/models` full CRUD + test + dim editing (new index appears); Collections create dialog enforces model selection; `/upload` tabs all functional; collections page copy reflects the registry.
15. Existing-DB migration: old `vector(1024)` column converges on boot (typmod dropped, **bare `ix_chunks_hnsw` dropped unconditionally even on already-converged DBs**, 1024 vectors intact + searchable via the new partial index); fresh databases boot clean with the untyped column (no bare-index DDL remains).

---

## 8. Step-by-Step Implementation Sequence

| # | Step | Files |
|---|---|---|
| 0 | Save this plan to `PLAN.md` (repo root) | `PLAN.md` |
| 1 | Schema DDL (registry table, untyped embedding, **delete bare `ix_chunks_hnsw` DDL from both copies**, pg_attribute migration block with unconditional legacy-index drop, 1024 partial index) + store struct + methods + `EnsureDimIndex` + `ResolveCollectionModel` + seed fn (with index provisioning) | `internal/store/schema.sql`, `deploy/schema.sql`, `internal/store/models.go`, `internal/store/embeddingmodels.go` (new) |
| 2 | Config hard cut + seed block | `internal/config/config.go`, `config_test.go` |
| 3 | `EmbedSpec`/`NewEmbedClient`, openai branch, plane URLs, `expectedDim` guard, `Probe`, unit tests | `internal/pipeline/tei_client.go`, new `tei_client_test.go` |
| 4 | Consumer rewiring: embedder dim guard, `embedQueryFn` (returns model), api/mcp `Deps.EmbedQuery`, search/mcp callers, `HybridSearch` dim param + cast CTE + 503 backstop, health/pipeline re-pointing | `internal/service/embedder.go`, `cmd/lybrix-server/helpers.go`, `main.go`, `internal/api/{server,search,system}.go`, `internal/mcp/{server,tools}.go`, `internal/store/search.go` |
| 5 | Seed-on-boot hook (inside `infra`) | `cmd/lybrix-server/main.go` |
| 6 | Registry REST: models.go (incl. dim confirmation), routes, scope table | `internal/api/models.go` (new), `server.go`, `middleware.go` |
| 7 | Collections mandatory binding + rebind endpoint | `internal/api/collections.go`, `internal/store/embeddingmodels.go` |
| 8 | Ingestion: disk-streaming `ingestFromStream` (temp file, PDF early stop, 64 MiB EPUB ceiling, multipart PUT), `fetch-url` with **dial-time SSRF transport (DialContext + Control, shared across redirects)**, OPDS browse (Atom parse) + sync (Basic Auth, per-item results) | `internal/api/ingest.go` (new), `internal/api/documents.go` (refactor verify into shared helpers) |
| 9 | EPUB pipeline branch: mime gate at verify, splitter branch (skip pdfcpu/bookmarks, single synthetic shard), parser docling-only routing, retry-ladder no-halving rule | `internal/api/ingest.go`, `internal/service/splitter.go`, `internal/pipeline/parser.go`, `internal/service/parser.go` |
| 10 | Snapshot ×2 + parity/scope test updates (25 paths) + handler unit tests | `internal/api/openapi_snapshot.json`, `services/web/openapi.json`, `api_test.go` |
| 11 | Store tests (testcontainers lane): seed idempotency + index provisioning, default uniqueness, delete 409s, resolution fallbacks, multi-dim round-trip (768 + 1024 coexist, both searchable), typmod-migration convergence **incl. bare-index drop on converged DBs**, dim-mismatch backstop | `internal/store/store_test.go` / new `embeddingmodels_test.go` |
| 12 | Web: regenerate client → queries hook → nav → models page/manager → collections create dialog + copy → upload hub (3 tabs) → admin proxy DELETE | `services/web/*` per §4 |
| 13 | Compose/env cleanup + `.env.example` + CLAUDE.md note | `deploy/docker-compose.yml`, `deploy/.env.example`, `CLAUDE.md` |
| 14 | Quality gates (documented acceptance): `go test ./...`, `make lint`, `make vet`, `npm run build && npm run lint` (web); live E2E per the verification script | — |

## Verification (end-to-end)

```bash
go test ./... && make lint && make vet
cd services/web && npm run generate-client && npm run build && npm run lint
make up-core
# 1) GET /v1/models → one seeded row (is_default, tei, bge-m3, dim 1024); \di ix_chunks_hnsw_1024
# 2) POST /v1/models {vector_dim: 768, …} → 201 (or 422 dim-confirmation); bind → ix_chunks_hnsw_768
# 3) POST /v1/collections without embedding_model_id → 422; with id → inherits model/dim
# 4) Upload tab: drop 2 PDFs + 1 EPUB → all hash/PUT/commit in parallel → all ready
#    (EPUB doc: mime_type=application/epub+zip, page_count=1, one synthetic shard, docling-only)
# 5) URL tab: paste an arxiv PDF link → 202 → ready; re-fetch same URL → 409 duplicate;
#    a URL redirecting to a private address → 400 (dial-time SSRF guard)
# 6) OPDS tab: browse the Calibre-Web feed (Basic Auth) → catalog renders; sync one page
#    → books ingest into the chosen collection; each doc → ready
# 7) POST /v1/search per collection → hits via each collection's model/dim; cross-dim rows
#    silently excluded; GET /v1/system/health reflects the default model's query_url probe
# 8) /models page: edit dim (new index), test, set default, delete (bound → 409)
```
