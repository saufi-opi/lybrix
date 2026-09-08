"""MCP tool logic (PRD §7.2): bounds, clamps, citation triples."""

from __future__ import annotations

import pytest
from mcp_server.server import _clamp_top_k, read_pages_impl, search_impl

from tests.conftest import make_settings
from tests.helpers import FakeSession


def _settings():
    return make_settings()


def test_clamp_top_k_rejects_zero():
    with pytest.raises(ValueError):
        _clamp_top_k(_settings(), 0)


def test_clamp_top_k_caps_at_max():
    assert _clamp_top_k(_settings(), 999) == _settings().search_max_top_k


class FakeQdrant:
    def query_points(self, **kw):
        pass  # not reached: query_embedder raises first


def test_search_requires_embedder():
    """search_impl calls the embedder before touching Qdrant — with a
    failing embedder it must raise before any vector call."""
    def boom(_q):
        raise RuntimeError("tei down")

    with pytest.raises(RuntimeError):
        search_impl(FakeSession(), FakeQdrant(), boom, _settings(), "query")


def _chunk(i, doc_id, seq=0, page=5):
    import uuid

    from core.db.models import Chunk

    return Chunk(
        id=uuid.uuid4(),
        doc_id=uuid.UUID(doc_id),
        chunk_hash=f"hash{i}",
        seq=seq,
        text=f"chunk {i}",
        token_count=3,
        page_start=page,
        page_end=page,
    )


def test_read_pages_caps_at_max():
    import uuid

    from core.db.models import Document

    doc_id = str(uuid.uuid4())
    doc = Document(
        id=uuid.UUID(doc_id),
        title="T",
        source_uri="s3://raw/x.pdf",
        content_sha256="0" * 64,
        page_count=400,
    )
    session = FakeSession(documents=[doc])
    out = read_pages_impl(session, doc_id, 1, 400, _settings())
    assert out["page_end"] == 1 + _settings().read_pages_max - 1
    assert out["truncated_to"] == _settings().read_pages_max
