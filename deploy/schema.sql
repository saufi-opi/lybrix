-- deploy/schema.sql — Lybrix 2.0 unified ParadeDB schema.
-- Embedded in the lybrix-server binary and applied idempotently at boot
-- (advisory-lock guarded). Replaces the 1.0 alembic chain; the hard cut
-- starts from a fresh database, so there is no data migration.
--
-- Blueprint §3 schema extended with every column the OpenAPI contract
-- reads (DocumentOut.byte_size, author, etc. — names match 1.0 exactly so
-- the web client needs no remap).

CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pg_search;

CREATE TABLE IF NOT EXISTS collections (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    embedding_model TEXT NOT NULL DEFAULT 'BAAI/bge-m3',
    vector_dim INT NOT NULL DEFAULT 1024,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS documents (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    collection_id TEXT REFERENCES collections(id),
    title TEXT,
    author TEXT,
    filename TEXT,
    byte_size BIGINT,
    source_uri TEXT NOT NULL,
    content_sha256 CHAR(64) NOT NULL,
    page_count INT,
    mime_type TEXT DEFAULT 'application/pdf',
    state TEXT NOT NULL DEFAULT 'uploaded'
        CHECK (state IN ('uploaded','splitting','parsing','embedding','indexing',
                         'ready','failed','partial','archived')),
    error_code TEXT,
    error_detail TEXT,
    total_shards INT,
    shards_done INT NOT NULL DEFAULT 0,
    shards_failed INT NOT NULL DEFAULT 0,
    chunk_count INT,
    completeness NUMERIC(5,4),
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    uploaded_by TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ready_at TIMESTAMPTZ,
    CONSTRAINT uq_doc_content UNIQUE (collection_id, content_sha256)
);
CREATE INDEX IF NOT EXISTS ix_documents_state ON documents (state);
CREATE INDEX IF NOT EXISTS ix_documents_updated ON documents (updated_at);

CREATE TABLE IF NOT EXISTS shards (
    doc_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    idx INT NOT NULL,
    page_start INT NOT NULL,
    page_end INT NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending','running','done','failed','skipped')),
    attempts INT NOT NULL DEFAULT 0,
    needs_ocr BOOLEAN NOT NULL DEFAULT FALSE,
    mean_chars_per_page REAL,
    parsed_uri TEXT,
    worker_id TEXT,
    lease_until TIMESTAMPTZ,
    duration_ms INT,
    peak_rss_mb INT,
    done_at TIMESTAMPTZ,
    error_code TEXT,
    error_detail TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (doc_id, idx)
);
CREATE INDEX IF NOT EXISTS ix_shards_done_at ON shards (done_at);

CREATE TABLE IF NOT EXISTS chunks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    doc_id UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    collection_id TEXT NOT NULL,
    chunk_hash CHAR(64) NOT NULL,
    parent_id UUID REFERENCES chunks(id),
    is_parent BOOLEAN NOT NULL DEFAULT FALSE,
    seq INT NOT NULL,
    page_start INT,
    page_end INT,
    heading_path TEXT[],
    header_breadcrumb TEXT,
    text TEXT NOT NULL,
    token_count INT NOT NULL,
    embedding vector,
    embedded_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_chunk_hash UNIQUE (doc_id, chunk_hash)
);

-- pgvector HNSW for dense retrieval — children only (parents carry no
-- vector). The column is UNTYPED (no typmod): dimensions are fully
-- arbitrary per registered model, so there is no bare index here — a bare
-- HNSW cannot even be built on a dimension-less column. One partial
-- expression HNSW exists per distinct dimension (seed dim 1024 below;
-- others created on demand by store.EnsureDimIndex). The predicate stops
-- maintenance of other-dimension rows; a bare cast would ERROR mismatched
-- inserts instead of excluding them.
CREATE INDEX IF NOT EXISTS ix_chunks_doc_seq ON chunks (doc_id, seq);
CREATE INDEX IF NOT EXISTS ix_chunks_hnsw_1024 ON chunks
    USING hnsw ((embedding::vector(1024)) vector_cosine_ops)
    WHERE is_parent = FALSE AND vector_dims(embedding) = 1024;

-- ParadeDB BM25 (pg_search) index. pg_search is assertion-based: wrap in a
-- DO block that checks whether the index's backing table already exists, so
-- re-running schema.sql is a no-op instead of erroring.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM paradedb.pd_search_index
        WHERE index_name = 'chunks_bm25_idx'
    ) THEN
        BEGIN
            CALL paradedb.create_bm25(
                index_name => 'chunks_bm25_idx',
                schema_name => 'public',
                table_name => 'chunks',
                key_field => 'id',
                text_fields => '{"text": {}, "header_breadcrumb": {}}'
            );
        EXCEPTION WHEN OTHERS THEN
            IF SQLERRM NOT LIKE '%already%' THEN
                -- pg_search versions differ on the catalog table name;
                -- fall back to trying a direct call and swallowing only
                -- "already exists" style errors.
                IF SQLERRM NOT LIKE '%duplicate%' AND SQLERRM NOT LIKE '%exists%' THEN
                    RAISE;
                END IF;
            END IF;
        END;
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS events (
    id BIGSERIAL PRIMARY KEY,
    doc_id UUID,
    shard_idx INT,
    level VARCHAR(8) NOT NULL,
    stage VARCHAR(16),
    code TEXT,
    message TEXT NOT NULL,
    context JSONB NOT NULL DEFAULT '{}'::jsonb,
    worker_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS ix_events_doc_created ON events (doc_id, created_at);
CREATE INDEX IF NOT EXISTS ix_events_level_created ON events (level, created_at);

CREATE TABLE IF NOT EXISTS api_keys (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT,
    key_hash CHAR(64) NOT NULL UNIQUE,
    scopes TEXT[] NOT NULL DEFAULT '{search}',
    collections TEXT[],
    expires_at TIMESTAMPTZ,
    revoked_at TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
-- 2.0 keeps BOTH revoked_at (1.0 semantics; KeyOut exposes it) and
-- expires_at — the blueprint's boolean `revoked` is replaced by the
-- timestamp.

CREATE TABLE IF NOT EXISTS key_usage (
    id BIGSERIAL PRIMARY KEY,
    api_key_id UUID NOT NULL REFERENCES api_keys(id) ON DELETE CASCADE,
    surface TEXT NOT NULL,   -- 'mcp' | 'api'
    action TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS ix_key_usage_key_created ON key_usage (api_key_id, created_at);

CREATE TABLE IF NOT EXISTS metrics_rollup (
    bucket TIMESTAMPTZ PRIMARY KEY,
    pages_parsed INT,
    shards_done INT,
    shards_failed INT,
    chunks_embedded INT,
    parse_p50_ms INT,
    parse_p95_ms INT,
    peak_rss_p95_mb INT,
    queue_depth JSONB,
    search_p95_ms INT,
    search_count INT
);

-- Model registry (2.0.1 — dynamic model providers). One row per usable
-- embedding model; collections bind to a row at creation. api_key is the
-- openai provider's secret — write-only, never serialized out.
CREATE TABLE IF NOT EXISTS embedding_models (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL UNIQUE,              -- display name, e.g. "bge-m3 @ ingest host"
    provider TEXT NOT NULL CHECK (provider IN ('tei','ollama','openai')),
    model_id TEXT NOT NULL,                 -- 'BAAI/bge-m3', 'nomic-embed-text', 'text-embedding-3-small'
    ingest_url TEXT NOT NULL,               -- ingest-plane endpoint (two-plane rule)
    query_url TEXT NOT NULL,                -- query-plane endpoint
    api_key TEXT,                           -- openai provider only; write-only
    vector_dim INT NOT NULL CHECK (vector_dim > 0 AND vector_dim <= 2000),  -- 2000 = pgvector HNSW cap
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

-- Collections binding. MANDATORY at creation from now on; the column stays
-- nullable so pre-registry collections keep resolving via the seeded
-- default row (read path only — see the resolution rules in PLAN.md §5).
ALTER TABLE collections ADD COLUMN IF NOT EXISTS embedding_model_id UUID REFERENCES embedding_models(id);
-- Legacy embedding_model / vector_dim columns remain (OpenAPI CollectionOut
-- compat) and are kept in sync FROM the bound row on create/bind.

-- One-time migration from the typed column: strip the typmod when present,
-- and drop the legacy bare index whenever it exists — including on
-- databases whose column already converged but which still carry
-- ix_chunks_hnsw (it cannot survive the untyped column and would reject
-- non-1024 inserts on mixed-dim data). Catalog views pg_attribute/pg_class
-- are used because information_schema.columns lacks atttypid/atttypmod.
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
