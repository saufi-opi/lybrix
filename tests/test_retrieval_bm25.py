"""Client-side BM25 hybrid wiring (Phase 1, revised): sparse-query prefetch
via the existing sparse_query param, off-mode regression guard, hybrid_search
forwarding, ensure_bm25_idf idempotence, and the bm25 encoder — all against
fakes, no network. Live-verified design facts (orchestrator probes on
qdrant/qdrant:v1.19.1): the server drops native `functions` configs and has
no QF/BM25 inference runtime, so retrieval is client-side encoded sparse
vectors + server-side idf modifier.
"""

from __future__ import annotations

import json

import pytest
from qdrant_client import models as qm
from retrieval.bm25 import SPARSE_DIM, encode_bm25, fnv1a, tokenize
from retrieval.qdrant import COLLECTION_NAME, ensure_bm25_idf, point_id_for
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


def _prefetch_key(p: qm.Prefetch) -> str:
    """Serialization signature used to compare prefetch lists."""
    return json.loads(p.model_dump_json(exclude_none=True)).__str__()


# --- bm25 encoder ------------------------------------------------------------


def test_tokenize_rules():
    assert tokenize("Hello World foo") == ["hello", "world", "foo"]
    # single-char tokens dropped: "c" (len 1), "a", "I"
    assert tokenize("C++ 3.5 a I") == []
    assert tokenize("C++ 3.5 ab 42") == ["ab", "42"]
    assert "a" not in tokenize("a bb ccc")
    assert tokenize("don't stop") == ["don", "stop"]


def test_fnv1a_pinned():
    """Deterministic 32-bit FNV-1a (pinned values for regression safety)."""
    assert fnv1a("") == 0x811C9DC5
    assert fnv1a("a") == 0xE40C292C  # standard FNV-1a("a")
    assert fnv1a("foobar") == 0xBF9CF968  # standard FNV-1a("foobar")


def test_encode_bm25_tf_weights():
    """value = 1.0 + log(tf) per unique token index."""
    vec = encode_bm25("error error error handling")
    tf_error = 3
    tf_handling = 1
    by_index = dict(zip(vec["indices"], vec["values"], strict=True))
    assert by_index[fnv1a("error") % SPARSE_DIM] == pytest.approx(1.0 + __import__("math").log(tf_error))
    assert by_index[fnv1a("handling") % SPARSE_DIM] == pytest.approx(1.0 + __import__("math").log(tf_handling))
    assert vec["indices"] == sorted(vec["indices"])
    assert all(0 <= i < SPARSE_DIM for i in vec["indices"])


def test_encode_bm25_collision_sums_values():
    """Two tokens hashing to the same index must SUM their counts before the
    log transform: tf combined 2 -> value 1.0 + log(2), not two entries."""
    # construct a genuine collision: brute-force two distinct tokens with the
    # same 21-bit index
    found = {}
    collision = None
    for n in range(200000):
        token = f"tok{n}"
        index = fnv1a(token) % SPARSE_DIM
        if index in found and found[index] != token:
            collision = (found[index], token, index)
            break
        found[index] = token
    assert collision is not None, "no collision found in 200k tokens — hash broken"
    token_a, token_b, index = collision
    vec = encode_bm25(f"{token_a} {token_b}")
    hits = [i for i in vec["indices"] if i == index]
    assert len(hits) == 1  # one slot, summed
    slot = dict(zip(vec["indices"], vec["values"], strict=True))[index]
    assert slot == pytest.approx(1.0 + __import__("math").log(2))


def test_encode_bm25_query_and_doc_same_encoding():
    """Query-side and document-side use the same encoding of the raw text."""
    q = encode_bm25("garbage collection")
    d = encode_bm25("Memory Management\n\nGarbage collection reclaims memory")
    q_indices = set(q["indices"])
    assert fnv1a("garbage") % SPARSE_DIM in q_indices
    assert fnv1a("collection") % SPARSE_DIM in q_indices
    assert fnv1a("garbage") % SPARSE_DIM in d["indices"]
    assert fnv1a("collection") % SPARSE_DIM in d["indices"]


# --- search_vectors: sparse_query mode ----------------------------------------


def test_search_vectors_sparse_query_builds_sparse_prefetch():
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


def test_search_vectors_off_mode_identical_prefetch_list():
    """Regression guard: sparse_query=None -> the exact prefetch list of
    today (single dense prefetch, same kwargs)."""
    qfilter = build_filter(collection_id="c1")
    client = FakeQdrantCapture()
    search_vectors(client, "chunks", [0.1, 0.2], None, 8, qfilter)
    kw = client.calls[0]
    assert len(kw["prefetch"]) == 1
    dense = qm.Prefetch(query=[0.1, 0.2], using="dense", limit=8, filter=qfilter)
    assert _prefetch_key(kw["prefetch"][0]) == _prefetch_key(dense)
    assert kw["query"] == qm.FusionQuery(fusion=qm.Fusion.RRF)
    assert kw["limit"] == 8
    assert kw["with_payload"] is True


def test_search_vectors_sparse_prefetch_carries_filter():
    qfilter = build_filter(collection_id="c1")
    client = FakeQdrantCapture()
    search_vectors(
        client, "chunks", [0.1, 0.2], {"indices": [1], "values": [1.0]}, 8, qfilter
    )
    assert client.calls[0]["prefetch"][1].filter is qfilter


# --- hybrid_search forwarding -----------------------------------------------


class FakeSession:
    def execute(self, stmt):
        class R:
            def scalars(self):
                return []

        return R()

    def get(self, model, pk):
        return None


def test_hybrid_search_forwards_sparse_query():
    client = FakeQdrantCapture()
    sparse = encode_bm25("error handling")
    hybrid_search(client, "chunks", FakeSession(), [0.1, 0.2], sparse, 8)
    prefetch = client.calls[0]["prefetch"]
    assert len(prefetch) == 2
    body = json.loads(prefetch[1].model_dump_json(exclude_none=True))
    assert body["query"] == sparse


def test_hybrid_search_off_mode_no_sparse():
    client = FakeQdrantCapture()
    hybrid_search(client, "chunks", FakeSession(), [0.1, 0.2], None, 8)
    assert len(client.calls[0]["prefetch"]) == 1


# --- qdrant.py: ensure_bm25_idf ----------------------------------------------


class FakeRestQdrant:
    """Records raw REST calls (the verified 1.19.0 passthrough surface)."""

    def __init__(self, sparse_vectors):
        self._sparse_vectors = sparse_vectors
        self.calls = []

    def _request(self, method, url, json=None):
        self.calls.append({"method": method, "url": url, "json": json})
        if method == "GET":
            return {"result": {"config": {"params": {"sparse_vectors": self._sparse_vectors}}}}
        if json and "sparse_vectors" in json:
            self._sparse_vectors.update(json["sparse_vectors"])
        return {"result": {"status": "ok"}}

    def request(self, type_=None, method=None, url=None, json=None, **kw):
        return self._request(method, url, json)

    @property
    def http(self):
        parent_ref = self

        class _Http:
            client = parent_ref

        return _Http()


def test_ensure_bm25_idf_absent_patches():
    client = FakeRestQdrant(sparse_vectors={"bm25": {}})
    ensure_bm25_idf(client)
    patch = [c for c in client.calls if c["method"] == "PATCH"]
    assert len(patch) == 1
    assert patch[0]["json"] == {"sparse_vectors": {"bm25": {"modifier": "idf"}}}
    assert patch[0]["url"] == f"/collections/{COLLECTION_NAME}"


def test_ensure_bm25_idf_present_is_noop():
    client = FakeRestQdrant(sparse_vectors={"bm25": {"modifier": "idf"}})
    ensure_bm25_idf(client)
    assert [c for c in client.calls if c["method"] != "GET"] == []  # no write


def test_ensure_bm25_idf_other_modifier_repairs():
    client = FakeRestQdrant(sparse_vectors={"bm25": {"modifier": "none"}})
    ensure_bm25_idf(client)
    patch = [c for c in client.calls if c["method"] == "PATCH"]
    assert len(patch) == 1
    assert patch[0]["json"]["sparse_vectors"]["bm25"] == {"modifier": "idf"}


def test_ensure_bm25_idf_missing_bm25_space_patches():
    client = FakeRestQdrant(sparse_vectors={})
    ensure_bm25_idf(client)
    patch = [c for c in client.calls if c["method"] == "PATCH"]
    assert len(patch) == 1


def test_point_id_for_stable():
    assert point_id_for("abc") == point_id_for("abc")
    assert point_id_for("abc") != point_id_for("abd")
