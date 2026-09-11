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

# libgomp1: torch/onnxruntime wheels. The X11 set + glib + libgl: docling's
# docling-ibm-models pulls opencv which dlopens libxcb/libX11/libXext at
# runtime — without them conversion dies with "libxcb.so.1: cannot open
# shared object file" (proven package set from remote-docling-worker image).
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        libgomp1 \
        libxcb1 \
        libx11-6 \
        libxext6 \
        libglib2.0-0 \
        libgl1 \
    && rm -rf /var/lib/apt/lists/*

# Non-root user FIRST — later layers are owned correctly from birth, so no
# `chown -R /app` is ever needed (it used to duplicate the whole venv into a
# new layer, ~1.9GB of pure image bloat).
RUN useradd --create-home --uid 1000 --shell /usr/sbin/nologin appuser

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
# Cache mount: uv's wheel cache stays in the builder, never bakes into a
# layer (the venv itself is the only thing the image keeps).
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --no-dev --package rag-workers --no-install-project

COPY --chown=appuser:appuser libs/ ./libs/
COPY --chown=appuser:appuser services/workers/ ./services/workers/
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --no-dev --package rag-workers

USER appuser

# No EXPOSE/HEALTHCHECK: workers are queue consumers, not servers.
# Compose supplies mem_limit: 8g / cpus: 2.0 / tmpfs for the parser (§6.3).
CMD ["python", "-m", "workers.janitor"]
