# syntax=docker/dockerfile:1
# services/mcp — MCP server image (streamable HTTP on /mcp).
FROM python:3.12-slim-bookworm

COPY --from=ghcr.io/astral-sh/uv:latest /uv /uvx /bin/

ENV UV_COMPILE_BYTECODE=1 \
    UV_LINK_MODE=copy \
    PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    PATH="/app/.venv/bin:$PATH"

# Non-root user FIRST (see workers/Dockerfile: chown -R after heavy layers
# duplicates the venv into a new layer).
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
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --no-dev --package rag-mcp --no-install-project

COPY --chown=appuser:appuser libs/ ./libs/
COPY --chown=appuser:appuser services/mcp/ ./services/mcp/
RUN --mount=type=cache,target=/root/.cache/uv \
    uv sync --frozen --no-dev --package rag-mcp

USER appuser

EXPOSE 8430

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD ["python", "-c", "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8430/health', timeout=3).read()"]

CMD ["python", "-m", "mcp_server"]
