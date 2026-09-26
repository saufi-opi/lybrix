# lybrix

Document Ingestion & Retrieval Platform — self-hosted ingestion of large
PDFs (300–800 pages) into a searchable corpus exposed via **MCP**, with an
admin web UI. Everything runs on Docker Compose; no Kubernetes.

**The spec lives in [`docs/prd.md`](docs/prd.md)** (16 sections). The
central design constraint: **a book is never a unit of work — a 16–24 page
shard is.** Every scaling, retry, progress, and memory decision follows
from that.

## Architecture (2.0 — Go rewrite)

```
upload → doc.split → doc.parse (×N shards) → doc.embed → ready
```

Each arrow is a Redis Stream consumer group; **ParadeDB** (PostgreSQL 17 +
pgvector + pg_search) is the state of record (Redis is dispatch only — the
janitor re-enqueues after a Redis loss). Dense (pgvector HNSW) and BM25
(pg_search) retrieval fuse in native SQL with weighted RRF. Ingest plane
(workers + `tei-ingest`) and query plane (`tei-query` serving MCP search)
never share an embedding server, so a backfill can't destroy query p99.

| Service | Role |
| --- | --- |
| `lybrix-server serve` | One binary: REST control plane (:8000) + six-tool MCP server (:8430) + janitor goroutine (lease reaper, retry escalation, PEL reclaim + DLQ, stuck detection, metrics rollup). The `janitor` subcommand exists for manual ops runs only — not a compose service (BACKLOG R-25) |
| `lybrix-splitter` | Chapter-aligned page-range sharding via pdfcpu |
| `lybrix-parser` | Two-tier: **anydoc** CGO fast path (born-digital, sub-100ms/shard) → **docling-serve** HTTP fallback (scanned/<50 chars/page) |
| `lybrix-embedder` | Stitch → hierarchical parent-child chunk (384-token children under 2048–4096-token parents) → TEI/Ollama batches → ParadeDB |
| `docling-serve` | Containerized layout analysis + OCR fallback |
| `web` | Next.js admin UI: dashboard, shard grid, upload, logs, retry tiers (unchanged contract) |
| `tei-ingest` / `tei-query` | HuggingFace TEI — separate batch budgets per plane |
| `paradedb` / `redis` / `minio` | State + dispatch + objects (Qdrant removed in 2.0) |

## Repo layout

```
lybrix/
├── cmd/             # lybrix-server (serve/splitter/parser/embedder/janitor/
│                    # keys/migrate) + lybrix-eval (golden-set harness)
├── internal/        # config, errors, logging, queue, objectstore, store
│                    # (pgx + pgvector + pg_search), api, mcp, pipeline,
│                    # service (worker handlers), worker (runner/janitor)
├── third_party/     # anydoc-go — the in-process CGO parser binding
├── deploy/          # docker-compose, schema.sql (embedded in the binary),
│                    # Dockerfile.lybrix, .env.example
├── scripts/         # build-anydoc-lib.sh, eval/ (golden set + results)
├── services/web/    # Next.js 15 admin UI + MCP Playground (unchanged)
└── docs/            # prd.md, adr/, BACKLOG.md
```

## Quick start

```bash
cp deploy/.env.example .env && $EDITOR .env

make up-core      # paradedb + redis + lybrix-server serve + web
make up-ingest    # workers + docling-serve + tei planes (MinIO lives here)
make down-ingest  # docker compose down on the ingest profile — stops consumers immediately
```

The schema applies automatically at serve boot (embedded `deploy/schema.sql`).
Create the first admin key with `make keys-bootstrap`, then upload through
the UI at :3000 (presigned direct-to-MinIO PUTs).

## Commands

```bash
make test         # go test ./...
make lint         # golangci-lint run
make vet          # go vet ./...
make build        # go build ./...

# eval (golden set, unchanged from 1.0):
go build -o bin/lybrix-eval ./cmd/lybrix-eval
LYBRIX_MCP_URL=... LYBRIX_MCP_TOKEN=... bin/lybrix-eval run \
  --dataset scripts/eval/datasets/seed.jsonl --top-k 8 --label baseline
```

The web UI has its own toolchain in `services/web/` (`npm run dev|build`,
`npm run lint`, `npm run generate-client` from the checked-in
`services/web/openapi.json` snapshot, which the Go server serves
byte-identical at `/openapi.json`).

## CI

`.github/workflows/ci.yml` runs `go test ./...` (non-CGO stub lane),
`golangci-lint`, `govulncheck`, and docker build validation for
`lybrix-server` + `web` on every push; `:edge` images publish from `main`,
semver tags publish versioned images. The `-tags anydoc` fast path is built
on the ingest host where `scripts/build-anydoc-lib.sh` has installed the
Rust static archive.
