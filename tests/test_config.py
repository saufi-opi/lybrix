"""Settings validation (PRD §12 core/config.py)."""

from __future__ import annotations

import pytest

from tests.conftest import make_settings


def test_defaults_load():
    s = make_settings()
    assert s.shard_pages == 20
    assert s.embed_model == "BAAI/bge-m3"
    assert s.embed_dim == 1024
    assert s.parser_soft_rss_mb == 6144
    assert s.search_max_top_k == 25
    assert s.read_pages_max == 30


def test_default_top_k_le_max():
    s = make_settings(search_default_top_k=8, search_max_top_k=25)
    assert s.search_default_top_k <= s.search_max_top_k


def test_top_k_violation_rejected():
    # pydantic wraps the model_validator's SettingsError in ValidationError,
    # which (like SettingsError) is a ValueError subclass.
    with pytest.raises(ValueError):
        make_settings(search_default_top_k=50, search_max_top_k=25)


def test_two_tei_urls_independent():
    """§4.3: ingest and query planes must never share one embedding server."""
    s = make_settings(tei_ingest_url="http://a:80", tei_query_url="http://b:80")
    assert s.tei_ingest_url != s.tei_query_url


def test_shard_pages_bounds():
    with pytest.raises(ValueError):  # pydantic ValidationError ⊂ ValueError
        make_settings(shard_pages=2)  # below ge=4


def test_embed_ctx_budget_default_and_plumbs():
    """R-16: 1900 is the ollama pin; TEI deployments raise it via env."""
    s = make_settings()
    assert s.embed_ctx_budget == 1900
    s2 = make_settings(embed_ctx_budget=8000)
    assert s2.embed_ctx_budget == 8000


def test_max_document_pages_default():
    """R-21: PRD §11 page-count cap, enforced at commit."""
    s = make_settings()
    assert s.max_document_pages == 800


def test_embed_query_prefix_default():
    """Minor 5: asymmetric-query prefix; empty disables."""
    s = make_settings()
    assert s.embed_query_prefix == "search_query: "
    s2 = make_settings(embed_query_prefix="")
    assert s2.embed_query_prefix == ""
