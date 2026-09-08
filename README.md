# rag-platform

Document Ingestion & Retrieval Platform — self-hosted ingestion of large
PDFs (300–800 pages) into a searchable corpus exposed via **MCP**, with an
admin web UI. Everything runs on Docker Compose; no Kubernetes.

**The spec lives in [`docs/prd.md`](docs/prd.md)** (16 sections). The
central design constraint: **a book is never a unit of work — a 16–24 page
shard is.** Every scaling, retry, progress, and memory decision follows
from that.

## Architecture

```
upload → doc.split → doc.parse (×N shards) → doc.embed → ready
```

Each arrow is a Redis Stream consumer group; Postgres is the state of
record (Redis is dispatch only — the janitor re-enqueues after a Redis
loss). Ingest plane (workers + `tei-ingest`) and query plane (`mcp` +
`tei-query`) never share an embedding server, so a backfill can't destroy
query p99 (PRD §4.3).

| Service | Role |
| --- | --- |
| `api` | Control-plane REST (presign/commit uploads, documents, search, SSE) |
| `worker-splitter` | Chapter-aligned page-range sharding (no Docling) |
| `worker-parser` | Docling per shard — the heavy one, 8GB mem cap, soft-RSS guard |
| `worker-embedder` | Stitch → chunk → TEI → Qdrant upsert (idempotent by chunk hash) |
| `worker-janitor` | Lease reaper, retry escalation, stuck detection, TTL |
| `mcp` | Six tools: search, list_collections, list_documents, get_document, read_pages, get_chunk_context |
| `web` | Next.js admin UI: dashboard, shard grid, upload, logs, retry tiers |
| `tei-ingest` / `tei-query` | HuggingFace TEI — separate batch budgets per plane |
| `postgres` / `redis` / `minio` / `qdrant` / `caddy` | State + edge |

## Repo layout

```
rag-platform/
├── libs/            # core (config/db/queue/storage/obs), parsing, chunking,
│                    # embedding, retrieval — shared, service-agnostic
├── services/        # api, workers (one image, four commands), mcp, web
├── migrations/      # alembic (§5 schema)
├── deploy/          # docker-compose base + gpu/dev overlays, Caddy, MinIO init
├── scripts/         # backfill, reembed, reindex, health
├── tests/           # DB-free unit suite (42 tests)
└── docs/prd.md      # the PRD — source of truth
```

## Quick start

```bash
cp .env.example .env && $EDITOR .env

make up          # GPU=0 make up for CPU-only TEI
make scale N=8   # parser replicas — the throughput dial
make drain       # stop consumers, let in-flight shards finish
```

First boot runs `migrate` automatically. Then upload through the UI at
`:80` (or `python -m scripts.backfill ./pdfs --collection my-collection`).

## MCP client config

```json
{
  "mcpServers": {
    "rag-platform": {
      "url": "https://your-host/mcp",
      "headers": { "Authorization": "Bearer <api-key>" }
    }
  }
}
```

## Development

```bash
uv sync --locked --group dev --all-packages
make test        # pytest (42 unit tests, no live services needed)
make lint        # ruff
make dev         # hot-reload overlay with exposed ports
```

CI (`.github/workflows/ci.yml`) mirrors the books-rag pattern: test + ruff
+ pip-audit + per-service docker build validation on every push; `:edge`
images published from `main`; semver tags publish versioned images and
move `latest`.

## Status

M1 (spine) is implemented: full pipeline code path, compose stack, MCP
surface, admin UI skeleton, unit suite. Retry-ladder sub-sharding,
reranking, and the metrics rollup writer are stubbed for M2/M3 per the
PRD milestone plan (§15).
