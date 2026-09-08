# syntax=docker/dockerfile:1
# services/workers — the heavy image: torch (CPU pinned) + docling baked in.
# One image build, four commands (§12): python -m workers.{splitter,parser,embedder,janitor}.
FROM python:3.12-slim-bookworm

COPY --from=ghcr.io/astral-sh/uv:latest /uv /uvx /bin/

ENV UV_COMPILE_BYTECODE=1 \
    UV_LINK_MODE=copy \
    PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    OMP_NUM_THREADS=2 \
    PATH="/app/.venv/bin:$PATH"

# libgomp1: required by the torch/onnxruntime wheels docling pulls in.
RUN apt-get update \
    && apt-get install -y --no-install-recommends libgomp1 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY pyproject.toml uv.lock README.md ./
COPY libs/core/pyproject.toml         libs/core/pyproject.toml
COPY libs/parsing/pyproject.toml      libs/parsing/pyproject.toml
COPY libs/chunking/pyproject.toml     libs/chunking/pyproject.toml
COPY libs/embedding/pyproject.toml    libs/embedding/pyproject.toml
COPY libs/retrieval/pyproject.toml    libs/retrieval/pyproject.toml
COPY services/api/pyproject.toml      services/api/pyproject.toml
COPY services/workers/pyproject.toml  services/workers/pyproject.toml
COPY services/mcp/pyproject.toml      services/mcp/pyproject.toml
# Heavy sync in its own cacheable layer: torch-cpu + docling ~1.5GB.
RUN uv sync --frozen --no-dev --package rag-workers --no-install-project

COPY libs/ ./libs/
COPY services/workers/ ./services/workers/
RUN uv sync --frozen --no-dev --package rag-workers

# Non-root; workers run as nobody.
RUN useradd --create-home --uid 1000 --shell /usr/sbin/nologin appuser \
    && chown -R appuser:appuser /app
USER appuser

# No EXPOSE/HEALTHCHECK: workers are queue consumers, not servers.
# Compose supplies mem_limit: 8g / cpus: 2.0 / tmpfs for the parser (§6.3).
CMD ["python", "-m", "workers.janitor"]
