"""Shared fixtures: a Settings factory with safe defaults so tests never
read the host environment."""

from __future__ import annotations

import pytest
from core.config import Settings


def make_settings(**overrides) -> Settings:
    defaults = dict(
        database_url="postgresql+psycopg://rag:rag@localhost:5432/rag",
        redis_url="redis://localhost:6379/0",
        s3_endpoint="http://localhost:9000",
        s3_access_key="test",
        s3_secret_key="test",
        qdrant_url="http://localhost:6333",
        tei_ingest_url="http://localhost:8081",
        tei_query_url="http://localhost:8082",
        mcp_api_key="x" * 32,
        _env_file=None,  # never pick up a developer's local .env
    )
    defaults.update(overrides)
    return Settings(**defaults)


@pytest.fixture
def settings() -> Settings:
    return make_settings()
