"""MCP tool logic (PRD §7.2): bounds, clamps, citation triples."""

from __future__ import annotations

import uuid

import pytest
from mcp_server.auth import AuthError, assert_collection_allowed
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


# --- BM25 flag threading (Phase 1) ------------------------------------------


class _EmptyScalars:
    def scalars(self):
        return []


class _EmptyResult:
    def scalars(self):
        return []


class _CaptureSession(FakeSession):
    """Enough session surface for search_impl → hybrid_search; hybrid_search
    itself is faked out so the test captures exactly what reaches it."""

    def execute(self, stmt):
        return _EmptyResult()


@pytest.fixture
def capture_hybrid(monkeypatch):
    captured = {}

    def fake_hybrid_search(qdrant, collection, session, dense_query, sparse_query,
                           top_k, collection_id=None, doc_id=None, bm25_text=None):
        captured.update(
            collection=collection, dense_query=dense_query, sparse_query=sparse_query,
            top_k=top_k, collection_id=collection_id, bm25_text=bm25_text,
        )
        return []  # no hits: search_impl returns []

    monkeypatch.setattr("retrieval.search.hybrid_search", fake_hybrid_search)
    return captured


def test_search_impl_flag_off_passes_bm25_none(capture_hybrid):
    """RETRIEVAL_BM25_ENABLED absent/false -> bm25_text=None reaches
    hybrid_search (byte-identical dense-only behavior)."""
    settings = make_settings(retrieval_bm25_enabled=False)
    def embed(_q):
        return [0.1, 0.2]

    out = search_impl(_CaptureSession(), FakeQdrant(), embed, settings, "some query")
    assert out == []
    assert capture_hybrid["bm25_text"] is None
    assert capture_hybrid["collection"] == "chunks"


def test_search_impl_flag_on_passes_query_text(capture_hybrid):
    settings = make_settings(retrieval_bm25_enabled=True)
    def embed(_q):
        return [0.1, 0.2]

    search_impl(_CaptureSession(), FakeQdrant(), embed, settings, "some query")
    assert capture_hybrid["bm25_text"] == "some query"


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


def _key(collections):
    from core.db.models import ApiKey

    return ApiKey(
        id=uuid.uuid4(),
        name="k",
        key_hash="0" * 64,
        scopes=["search"],
        collections=collections,
    )


def test_search_rejects_out_of_scope_collection():
    key = _key(["col-a"])
    with pytest.raises(AuthError, match="not scoped"):
        assert_collection_allowed(key, "col-b")
    assert_collection_allowed(key, "col-a")  # in scope: passes
    # empty collections list = unrestricted
    unrestricted = _key(None)
    assert_collection_allowed(unrestricted, "col-b")
    # None collection is never restricted
    assert_collection_allowed(key, None)
