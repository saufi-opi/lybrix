# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Document ingestion & retrieval platform: self-hosted ingestion of large PDFs (300–800 pages) into a searchable corpus exposed via MCP, with an admin web UI. Everything runs on Docker Compose.

**`docs/prd.md` is the source of truth** (16 sections). **`docs/BACKLOG.md` is the incident ledger** — a new confirmed bug lands there as a row (symptom → root cause → fix commit) *before* any code change.

Central design constraint driving everything: **a book is never a unit of work — a 16–24 page shard is.** Every scaling, retry, progress, and memory decision follows from that.

## Commands

```bash
# One-time / after pulling: sync the uv workspace (root is a non-packaged shell;
# --all-packages is required or workspace members won't be in the venv)
uv sync --locked --group dev --all-packages

make test        # pytest tests/ — root suite only
uv run pytest services/workers/tests/ -q   # workers suite is NOT in make test
uv run pytest tests/ services/workers/tests/ -q   # everything (~269 tests)
uv run pytest tests/test_chunking.py -q    # single file
uv run pytest tests/test_chunking.py -k name_of_test   # single test

make lint        # uvx ruff check . (line-length 100, py312 target)

make up          # docker compose up (both profiles) — first boot runs migrate
make up-core     # profile core only (postgres/redis/minio/qdrant/api/mcp/web)
make up-ingest   # profile ingest only (workers + tei-*)
make scale N=8   # parser replicas — the throughput dial
make drain       # stop consumers, let in-flight shards finish
make logs-core / logs-ingest / ps-core / ps-ingest
```

Python is pinned to 3.12 (`.python-version` + `requires-python = ">=3.12"` in every pyproject) — matching CI and the `python:3.12-slim-bookworm` Dockerfiles. If commands fail with "No such file or directory" after a repo copy/rename (stale `.venv` shebangs), re-run the sync command above.

Config lives in `.env` (copy from `.env.example`). Tests are DB-free: fixtures use a `Settings` factory with `_env_file=None` so they never read a developer's `.env` or touch live services.

Web UI (Next.js) has its own toolchain in `services/web/`: `npm run dev|build|lint`.

CI (`.github/workflows/ci.yml`) runs test + ruff + pip-audit + per-service docker build validation on every push; `:edge` images publish from `main`, semver tags publish versioned images.

## Architecture

```
upload → doc.split → doc.parse (×N shards) → doc.embed → ready
```

- Each arrow is a **Redis Stream consumer group**; **Postgres is the state of record, Redis is dispatch only.** If Redis is wiped, the janitor re-enqueues everything not in a terminal state. This is what makes the system resumable (crash loses ≤ 1 shard).
- **Two planes that never share an embedding server:** ingest plane (`worker-*` + `tei-ingest`, throughput-optimised) and query plane (`mcp` + `tei-query`, latency-optimised). TEI batches by total token count, so a shared TEI container lets a 65k-token ingest batch destroy query p99 — never collapse them into one.
- Retry/ack semantics live in **exactly one place**: `services/workers/src/workers/runner.py`. Every worker uses this generic consumer loop. Handlers raise `PlatformError` (taxonomy in `libs/core/src/core/errors.py`); retryability comes from the error class, never string matching. A failing job is never ACKed or retried in the loop — it stays in the Redis PEL, and the janitor reclaim enforces the delivery cap (quarantine → DLQ events row → XACK/XDEL). The PEL's `times_delivered` is the single attempt counter.

### Repo layout

```
libs/       shared, service-agnostic packages (uv workspace members):
            core (config/db/queue/streams/keys/events/observability),
            parsing (splitter/stitch/converter/ocr_gate/memory),
            chunking, embedding, retrieval (qdrant/bm25/rerank/search)
services/   api (FastAPI control plane), workers (ONE image, four entrypoints:
            splitter/parser/embedder/janitor via python -m workers.<name>),
            mcp (six tools), web (Next.js admin UI)
migrations/ alembic
deploy/     docker-compose base + dev/gpu overlays, Caddy, MinIO init
scripts/    backfill, reembed, reindex, ops_backfill_sparse, eval harness
            (scripts/eval — golden-set retrieval eval against the live MCP
            endpoint; must run from repo root; see scripts/eval/README.md)
```

Workers deploy as one image with four commands (`python -m workers.splitter|parser|embedder|janitor`); adding a fifth worker means adding a module, not an image.

### Status / milestone awareness

Per PRD §15 and the README: M1 spine + durability work and the retrieval rerank stage are implemented; some M2/M3 items (retry-ladder sub-sharding, metrics rollup writer) may still be stubbed — check the README Status section and BACKLOG.md before assuming a feature is real. Parser memory is bounded by sharding **plus** `PARSER_RECYCLE_AFTER` (clean process exit on a job boundary so docker's restart policy revives it with fresh memory).
