"""Retry-ladder sub-sharding (Phase 5, PRD §6.3) — attempts are not identical.

Ladder:
- attempt 2: re-split the shard into 4 sub-shards (quarter-split)
- attempt 3: single-page sub-shards
- attempt >=4: text-only convert (table structure off)
- final failure path unchanged (janitor escalation handles attempts >= cap)

Fake session/redis pattern of test_embedder_retry_cap.py; the converter is
monkeypatched (no docling, no transformers).
"""

from __future__ import annotations

import uuid
from unittest.mock import MagicMock, patch

from workers.parser import _ladder_action, _split_and_requeue, handle_parse


def _settings(shard_pages=20):
    s = MagicMock()
    s.shard_pages = shard_pages
    s.parser_pdf_cache_dir = None
    s.parsing_do_table_structure = True
    return s


def _doc(total_shards=4):
    doc = MagicMock()
    doc.id = uuid.uuid4()
    doc.source_uri = f"s3://raw/{doc.id}.pdf"
    doc.total_shards = total_shards
    return doc


def _shard(attempts, span=20, idx=0):
    shard = MagicMock()
    shard.attempts = attempts
    shard.idx = idx
    shard.page_start = 1
    shard.page_end = span
    return shard


class FakeSession:
    """Records shard-row writes via the repo helpers (repo is monkeypatched
    to record instead of touching Postgres)."""

    def __init__(self, doc):
        self.doc = doc
        self.flushed = 0
        self.added = []

    def add(self, obj):
        self.added.append(obj)

    def add_all(self, objs):
        self.added.extend(objs)

    def flush(self):
        self.flushed += 1


def _redis():
    r = MagicMock()
    return r


def _repo_patches(session, doc):
    """Patch the repo helpers _split_and_requeue calls."""
    calls = {"skip": [], "insert": [], "next_idx": 100}

    def fake_next_idx(s, doc_id):
        return calls["next_idx"]

    def fake_skip(s, doc_id, idx):
        calls["skip"].append(idx)

    def fake_insert(s, doc_id, bounds, start_idx=0):
        calls["insert"].append((list(bounds), start_idx))
        return len(bounds)

    p1 = patch("workers.parser.repo.next_shard_idx", fake_next_idx)
    p2 = patch("workers.parser.repo.skip_shard", fake_skip)
    p3 = patch("workers.parser.repo.insert_shards", fake_insert)
    return calls, [p1, p2, p3]


def _xadd_capture():
    calls: list = []
    return calls


def test_attempt2_span20_quarter_split():
    doc = _doc(total_shards=4)
    shard = _shard(attempts=2, span=20)
    s = _settings(shard_pages=20)
    session = FakeSession(doc)
    redis = _redis()
    calls, patches = _repo_patches(session, doc)
    xadds = _xadd_capture()
    with patch("workers.parser.streams.xadd_job", lambda r, stream, payload: xadds.append((stream, payload))):
        for p in patches:
            p.start()
        try:
            _split_and_requeue(session, redis, doc, shard, s)
        finally:
            for p in patches:
                p.stop()
    # 4 disjoint sub-shard rows of 5 pages each
    (bounds, start_idx), = calls["insert"]
    assert len(bounds) == 4
    assert bounds == [(1, 5), (6, 10), (11, 15), (16, 20)]
    assert calls["skip"] == [0]
    # 4 ParseJob XADDs from the computed start_idx
    assert len(xadds) == 4
    from core.queue import contracts as contracts_mod

    jobs = [payload for _stream, payload in xadds]
    assert all(isinstance(j, contracts_mod.ParseJob) for j in jobs)
    assert [j.idx for j in jobs] == [100, 101, 102, 103]
    assert [j.page_start for j in jobs] == [1, 6, 11, 16]
    assert [j.page_end for j in jobs] == [5, 10, 15, 20]
    assert all(j.source_uri == doc.source_uri for j in jobs)
    # total_shards grows by the sub-shard count or book_settled never fires
    assert doc.total_shards == 8
    # SHARD_RESPLIT event
    assert session.flushed >= 1


def test_attempt3_single_page_subshards():
    doc = _doc(total_shards=6)
    shard = _shard(attempts=3, span=6)
    s = _settings(shard_pages=20)
    session = FakeSession(doc)
    redis = _redis()
    calls, patches = _repo_patches(session, doc)
    xadds = _xadd_capture()
    with patch("workers.parser.streams.xadd_job", lambda r, stream, payload: xadds.append((stream, payload))):
        for p in patches:
            p.start()
        try:
            _split_and_requeue(session, redis, doc, shard, s)
        finally:
            for p in patches:
                p.stop()
    (bounds, start_idx), = calls["insert"]
    assert len(bounds) == 6
    # 6 disjoint single-page sub-shards tiling the span-6 parent
    assert bounds == [(1, 1), (2, 2), (3, 3), (4, 4), (5, 5), (6, 6)]
    assert len(xadds) == 6
    assert doc.total_shards == 12


def test_attempt4_text_only_converter_settings():
    """Attempt >=4 falls through with parsing_do_table_structure=False — the
    converter receives a degraded settings copy and the shard proceeds to the
    normal done/fail path."""
    doc = _doc()
    s = _settings(shard_pages=20)
    doc_id = doc.id
    session = MagicMock()
    redis = MagicMock()

    claimed = MagicMock()
    claimed.attempts = 4
    claimed.idx = 0
    session.execute.return_value.scalar_one_or_none.return_value = claimed

    doc_row = MagicMock()
    doc_row.id = doc_id
    doc_row.source_uri = "s3://raw/x.pdf"
    doc_row.total_shards = 1

    converter = MagicMock()
    verdict = MagicMock()
    verdict.needs_ocr = False

    model_copy_calls: list = []

    def fake_model_copy(update=None):
        model_copy_calls.append(update)
        degraded = MagicMock()
        degraded.parsing_do_table_structure = False
        return degraded

    s.model_copy = fake_model_copy

    captured_settings = {}

    def fake_get_converter(need_ocr, settings, builder):
        captured_settings["s"] = settings
        return converter

    with (
        patch("workers.parser.repo.get_document", return_value=doc_row),
        patch("workers.parser.repo.claim_shard", return_value=claimed),
        patch("workers.parser.repo.get_document", return_value=doc_row),
        patch("workers.parser.repo.mark_shard_done"),
        patch("workers.parser.repo.book_settled", return_value=False),
        patch("workers.parser.needs_ocr", return_value=verdict),
        patch("workers.parser.get_converter", side_effect=fake_get_converter),
        patch("workers.parser.converter_cache_key", return_value="k"),
        patch("workers.parser.check_rss_budget", return_value=0),
        patch("workers.parser.s3.download_to"),
        patch("workers.parser.s3.make_s3", return_value=MagicMock()),
        patch("workers.parser.s3.upload_text"),
        patch("workers.parser.s3.raw_key", return_value="k"),
        patch("workers.parser.s3.parsed_key", return_value="k"),
        patch("workers.parser.tempfile.mkdtemp", return_value="/tmp/parse-fake"),
        patch("workers.parser.Path.is_file", return_value=False),
        patch("workers.parser.streams.xadd_job"),
    ):
        # converter.convert returns a result whose document exports markdown
        converter.convert.return_value = MagicMock(
            document=MagicMock(export_to_markdown=lambda: "# ok")
        )
        handle_parse(session, {"doc_id": str(doc_id), "idx": 0, "page_start": 1, "page_end": 20}, redis, s)

    # the degraded copy was requested with table structure off
    assert model_copy_calls == [{"parsing_do_table_structure": False}]
    # the converter received the degraded copy
    assert captured_settings["s"].parsing_do_table_structure is False
    # the shared settings object is untouched
    assert s.parsing_do_table_structure is True


def test_small_span_attempt2_falls_to_text_only():
    """A span-2 attempt-2 shard is too small to quarter-split → text_only."""
    shard = _shard(attempts=2, span=2)
    s = _settings(shard_pages=20)
    assert _ladder_action(shard, s) == "text_only"


def test_attempt2_worthy_span_splits():
    shard = _shard(attempts=2, span=20)
    s = _settings(shard_pages=20)
    assert _ladder_action(shard, s) == "split"


def test_attempt3_any_span2_splits():
    shard = _shard(attempts=3, span=6)
    s = _settings(shard_pages=20)
    assert _ladder_action(shard, s) == "split"
    small = _shard(attempts=3, span=1)
    assert _ladder_action(small, s) == "text_only"


def test_embedder_orders_shards_by_page_start():
    """Sub-shards continue idx after the parent; page_start is the true
    document order — the embedder's done-shard query must order by it."""
    import inspect

    import workers.embedder as embedder_mod

    src = inspect.getsource(embedder_mod.handle_embed)
    assert "order_by(Shard.page_start)" in src
    assert "order_by(Shard.idx)" not in src
