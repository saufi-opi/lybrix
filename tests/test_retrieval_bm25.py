"""Native BM25 prefetch wiring (Phase 1): search_vectors bm25_text mode,
off-mode regression guard, legacy sparse_query preservation, hybrid_search
forwarding, and ensure_bm25_function idempotence — all against fakes, no
network. API shapes verified live against qdrant-client 1.19.0 (plan step 0):
Prefetch(query="<text>", using="bm25") is the native prefetch shape; function
create/patch go through raw REST because the typed models lack `functions`.
"""

from __future__ import annotations

import json

from qdrant_client import models as qm
from retrieval.qdrant import (
    BM25_FUNCTION_NAME,
    TEXT_FIELD,
    _bm25_function,
    ensure_bm25_function,
    point_id_for,
)
from retrieval.search import build_filter, hybrid_search, search_vectors


class FakeQueryResponse:
    def __init__(self, points):
        self.points = points


class FakeQdrantCapture:
    """Captures query_points kwargs; returns a canned fused list."""

    def __init__(self, points=None):
        self.calls = []
        self._points = points if points is not None else []

    def query_points(self, **kw):
        self.calls.append(kw)
        return FakeQueryResponse(self._points)


def _dense_prefetch(limit, qfilter=None):
    return qm.Prefetch(query=[0.1, 0.2], using="dense", limit=limit, filter=qfilter)


def _prefetch_key(p: qm.Prefetch) -> tuple:
    """Serialization signature used to compare prefetch lists."""
    return json.loads(p.model_dump_json(exclude_none=True)).__str__()


# --- search_vectors: bm25_text mode -----------------------------------------


def test_search_vectors_bm25_text_builds_text_prefetch():
    client = FakeQdrantCapture()
    search_vectors(client, "chunks", [0.1, 0.2], None, 8, bm25_text="error handling")
    prefetch = client.calls[0]["prefetch"]
    assert len(prefetch) == 2
    body = json.loads(prefetch[1].model_dump_json(exclude_none=True))
    assert body == {"query": "error handling", "using": "bm25", "limit": 8}


def test_search_vectors_off_mode_identical_prefetch_list():
    """Regression guard: bm25_text=None, sparse_query=None -> the exact
    prefetch list of today (single dense prefetch, same kwargs)."""
    qfilter = build_filter(collection_id="c1")
    client = FakeQdrantCapture()
    search_vectors(client, "chunks", [0.1, 0.2], None, 8, qfilter, None)
    kw = client.calls[0]
    assert len(kw["prefetch"]) == 1
    assert _prefetch_key(kw["prefetch"][0]) == _prefetch_key(
        _dense_prefetch(8, qfilter)
    )
    assert kw["query"] == qm.FusionQuery(fusion=qm.Fusion.RRF)
    assert kw["limit"] == 8
    assert kw["with_payload"] is True


def test_search_vectors_legacy_sparse_query_unchanged():
    client = FakeQdrantCapture()
    search_vectors(
        client, "chunks", [0.1, 0.2], {"indices": [3, 7], "values": [1.0, 2.0]}, 8
    )
    prefetch = client.calls[0]["prefetch"]
    assert len(prefetch) == 2
    body = json.loads(prefetch[1].model_dump_json(exclude_none=True))
    assert body == {
        "query": {"indices": [3, 7], "values": [1.0, 2.0]},
        "using": "bm25",
        "limit": 8,
    }


def test_search_vectors_bm25_text_wins_over_sparse_query():
    """bm25_text + sparse_query both set -> bm25_text wins, never both."""
    client = FakeQdrantCapture()
    search_vectors(
        client,
        "chunks",
        [0.1, 0.2],
        {"indices": [3], "values": [1.0]},
        8,
        bm25_text="text query",
    )
    prefetch = client.calls[0]["prefetch"]
    assert len(prefetch) == 2
    body = json.loads(prefetch[1].model_dump_json(exclude_none=True))
    assert body["query"] == "text query"  # not a SparseVector dict


def test_search_vectors_bm25_prefetch_carries_filter():
    qfilter = build_filter(collection_id="c1")
    client = FakeQdrantCapture()
    search_vectors(client, "chunks", [0.1, 0.2], None, 8, qfilter, bm25_text="q")
    prefetch = client.calls[0]["prefetch"]
    assert prefetch[1].filter is qfilter


# --- hybrid_search forwarding -----------------------------------------------


class FakeSession:
    def execute(self, stmt):
        class R:
            def scalars(self):
                return []

        return R()

    def get(self, model, pk):
        return None


def test_hybrid_search_forwards_bm25_text():
    client = FakeQdrantCapture()
    hybrid_search(client, "chunks", FakeSession(), [0.1, 0.2], None, 8, bm25_text="q text")
    prefetch = client.calls[0]["prefetch"]
    assert len(prefetch) == 2
    assert json.loads(prefetch[1].model_dump_json(exclude_none=True))["query"] == "q text"


def test_hybrid_search_off_mode_no_bm25():
    client = FakeQdrantCapture()
    hybrid_search(client, "chunks", FakeSession(), [0.1, 0.2], None, 8)
    assert len(client.calls[0]["prefetch"]) == 1


# --- qdrant.py: function definition + ensure_bm25_function -------------------


def test_bm25_function_shape():
    function = _bm25_function()
    assert function == {
        "name": "bm25",
        "function_type": "bm25",
        "input": [{"type": "text", "text_field": "text"}],
        "output": "bm25",
    }


class FakeRestQdrant:
    """Records raw REST calls (the verified 1.19.0 passthrough surface)."""

    def __init__(self, functions):
        self._functions = functions
        self.calls = []

    def _request(self, method, url, json=None):
        self.calls.append({"method": method, "url": url, "json": json})
        if method == "GET":
            return {"result": {"functions": self._functions}}
        if json and "functions" in json:
            self._functions = json["functions"]
        return {"result": {"status": "ok"}}

    def request(self, type_=None, method=None, url=None, json=None, **kw):
        return self._request(method, url, json)

    @property
    def http(self):
        parent_ref = self

        class _Http:
            client = parent_ref

        return _Http()


def test_ensure_bm25_function_absent_patches():
    client = FakeRestQdrant(functions=[])
    ensure_bm25_function(client)
    patch = [c for c in client.calls if c["method"] == "PATCH"]
    assert len(patch) == 1
    assert patch[0]["json"]["functions"] == [_bm25_function()]
    assert patch[0]["url"] == "/collections/chunks"


def test_ensure_bm25_function_present_matching_is_noop():
    client = FakeRestQdrant(functions=[_bm25_function()])
    ensure_bm25_function(client)
    # read-only inspection only: no PATCH (no request) is sent
    assert [c for c in client.calls if c["method"] != "GET"] == []


def test_ensure_bm25_function_present_mismatched_repairs():
    stale = dict(_bm25_function())
    stale["input"] = [{"type": "text", "text_field": "wrong_field"}]
    client = FakeRestQdrant(functions=[stale])
    ensure_bm25_function(client)
    patch = [c for c in client.calls if c["method"] == "PATCH"]
    assert len(patch) == 1
    assert patch[0]["json"]["functions"] == [_bm25_function()]


def test_point_id_for_stable():
    assert point_id_for("abc") == point_id_for("abc")
    assert point_id_for("abc") != point_id_for("abd")


def test_constants():
    assert BM25_FUNCTION_NAME == "bm25"
    assert TEXT_FIELD == "text"
