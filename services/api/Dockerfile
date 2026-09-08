# syntax=docker/dockerfile:1
# services/api — control-plane image.
# uv end to end: uv.lock is the single source of truth and the build fails
# closed (--frozen) if it's out of sync with pyproject.toml.
FROM python:3.12-slim-bookworm

COPY --from=ghcr.io/astral-sh/uv:latest /uv /uvx /bin/

ENV UV_COMPILE_BYTECODE=1 \
    UV_LINK_MODE=copy \
    PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    PATH="/app/.venv/bin:$PATH"

WORKDIR /app

# Workspace resolution needs the root project + every member's pyproject;
# only then can uv sync --package target this service's dependency subset.
COPY pyproject.toml uv.lock README.md ./
COPY libs/core/pyproject.toml         libs/core/pyproject.toml
COPY libs/parsing/pyproject.toml      libs/parsing/pyproject.toml
COPY libs/chunking/pyproject.toml     libs/chunking/pyproject.toml
COPY libs/embedding/pyproject.toml    libs/embedding/pyproject.toml
COPY libs/retrieval/pyproject.toml    libs/retrieval/pyproject.toml
COPY services/api/pyproject.toml      services/api/pyproject.toml
COPY services/workers/pyproject.toml  services/workers/pyproject.toml
COPY services/mcp/pyproject.toml      services/mcp/pyproject.toml
RUN uv sync --frozen --no-dev --package rag-api --no-install-project

COPY libs/ ./libs/
COPY services/api/ ./services/api/
RUN uv sync --frozen --no-dev --package rag-api

# Non-root.
RUN useradd --create-home --uid 1000 --shell /usr/sbin/nologin appuser \
    && chown -R appuser:appuser /app
USER appuser

EXPOSE 8000

HEALTHCHECK --interval=30s --timeout=5s --start-period=15s --retries=3 \
    CMD ["python", "-c", "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8000/v1/system/health', timeout=3).read()"]

CMD ["uvicorn", "api.main:app", "--host", "0.0.0.0", "--port", "8000"]
