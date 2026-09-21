"""Qdrant upsert paging (R-17) + doc-scoped point ids (R-12).

2026-09-11: a whole book's points went up in one wait=True request — the
most timeout-prone shape available; the incident was exactly a Qdrant
upsert timeout loop. upsert_chunks must page.
"""

from __future__ import annotations

import uuid

from retrieval.qdrant import point_id_for, upsert_chunks


class FakeQdrantUpsert:
    """Records upsert calls."""

    def __init__(self):
        self.calls = []

    def upsert(self, collection_name, points, wait):
        self.calls.append(
            {"collection_name": collection_name, "points": list(points), "wait": wait}
        )


def _points(n: int, doc_id=None) -> list[dict]:
    doc_id = doc_id or str(uuid.uuid4())
    return [
        {
            "chunk_hash": f"{i:064d}",
            "doc_id": doc_id,
            "collection_id": "c1",
            "vector": [0.1, 0.2],
            "page_start": i,
            "page_end": i,
            "heading_path": [],
        }
        for i in range(n)
    ]


def test_upsert_pages_500():
    client = FakeQdrantUpsert()
    n = upsert_chunks(client, _points(1201))
    assert n == 1201
    assert [len(c["points"]) for c in client.calls] == [500, 500, 201]
    assert all(c["wait"] is True for c in client.calls)
    assert all(c["collection_name"] == "chunks" for c in client.calls)


def test_upsert_empty_list_zero_calls():
    client = FakeQdrantUpsert()
    assert upsert_chunks(client, []) == 0
    assert client.calls == []


def test_upsert_pages_are_disjoint_and_ordered():
    client = FakeQdrantUpsert()
    pts = _points(600)
    upsert_chunks(client, pts)
    all_ids = [p.id for c in client.calls for p in c["points"]]
    expected = [point_id_for(pts[0]["doc_id"], p["chunk_hash"]) for p in pts]
    assert all_ids == expected  # disjoint, ordered, doc-scoped (R-12)
