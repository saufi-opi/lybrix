"""Tests for citation page metadata through the embed path (PRD G4).

shard page ranges flow stitch → chunk → PG/Qdrant: handle_embed builds
``(page_start, page_end)`` pairs from the idx-sorted done-shard rows and
hands them to stitch; chunks then carry page_start/page_end into the
Postgres insert. Back-compat: without ranges everything stays NULL.
"""

from __future__ import annotations

import uuid
from unittest.mock import MagicMock, patch

import parsing.stitch  # noqa: F401  (must exist before patch targets resolve)
from workers.embedder import handle_embed


def _doc():
    doc = MagicMock()
    doc.id = uuid.uuid4()
    doc.collection_id = "coll"
    doc.total_shards = 2
    doc.shards_failed = 0
    doc.state = "parsing"
    return doc


def _shard_rows(pages):
    rows = []
    for idx, (ps, pe) in enumerate(pages):
        row = MagicMock()
        row.idx = idx
        row.page_start = ps
        row.page_end = pe
        rows.append(row)
    return rows


def _run(doc, shard_rows, *, captured):
    """Run handle_embed with mocked externals; capture stitch/insert calls."""

    def session_execute(stmt, *a, **k):
        res = MagicMock()
        if "shards" in str(stmt):
            res.scalars.return_value.all.return_value = shard_rows
        else:
            captured["stmts"].append(stmt)
            res.scalars.return_value.all.return_value = []
        return res

    session = MagicMock()
    session.execute.side_effect = session_execute

    stitch_ret = {"texts": [], "markdown": "# A\nhello world", "pages": [1, 1]}

    def fake_stitch(docs, ranges=None):
        captured["stitch_ranges"] = ranges
        return stitch_ret

    class _Chunk:
        chunk_hash = "h" * 64
        seq = 0
        text = "hello world"
        token_count = 2
        heading_path = ("A",)
        page_start = 4
        page_end = 7

    tei = MagicMock()
    tei.embed.return_value = [[0.0, 0.0]]  # one vector for the one chunk
    # MagicMock(return_value=tei) also makes __enter__ return tei
    tei.__enter__.return_value = tei
    with (
        patch("workers.embedder.repo.get_document", return_value=doc),
        patch("workers.embedder.repo.set_doc_state"),
        patch("workers.embedder.s3.make_s3"),
        patch("workers.embedder.s3.parsed_key", return_value="k"),
        patch("parsing.stitch.load_shard_docs", MagicMock(return_value=[])),
        patch("parsing.stitch.stitch", MagicMock(side_effect=fake_stitch)),
        patch("workers.embedder.chunk_markdown", MagicMock(return_value=[_Chunk()])),
        patch("workers.embedder.drop_duplicate_neighbours", MagicMock(side_effect=lambda cs: cs)),
        patch("workers.embedder.TeiClient", MagicMock(return_value=tei)),
        patch("workers.embedder.upsert_chunks"),
        patch("workers.embedder.ensure_collection"),
        # QdrantClient is imported inside handle_embed → patch at source
        patch("qdrant_client.QdrantClient"),
    ):
        handle_embed(session, {"doc_id": str(doc.id)}, redis=MagicMock())


def test_handle_embed_passes_shard_page_ranges_to_stitch():
    doc = _doc()
    captured: dict = {"stmts": []}
    rows = _shard_rows([(1, 20), (21, 40)])
    _run(doc, rows, captured=captured)
    assert captured["stitch_ranges"] == [(1, 20), (21, 40)]


def test_page_fields_land_in_pg_insert():
    doc = _doc()
    captured: dict = {"stmts": []}
    rows = _shard_rows([(1, 20), (21, 40)])
    _run(doc, rows, captured=captured)
    # the pg_insert(Chunk) statements carry the chunk's page fields
    page_stmts = [s for s in captured["stmts"] if "page_start" in str(s)]
    assert page_stmts, "expected a PG insert with page_start"
