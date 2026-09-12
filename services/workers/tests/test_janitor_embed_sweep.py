"""Tests for the janitor settled-book embed sweep (EMBED_RESCUE).

Scenario under test: a parser died between committing the last shard and
XADDing the embed job — book stays state=parsing with all shards done.
The janitor must XADD an embed job for it; docs already ready/partial or
not settled must NOT be re-enqueued.

Dedup (2026-09-11 incident): the sweep ran every pass with no check for
an already-queued job — a book whose embed job sat unacked (stale PEL /
slow embed) was re-XADDed every 30s, flooding doc.embed with thousands
of duplicates. The sweep must skip books that already hold an undelivered
job on doc.embed.
"""

from __future__ import annotations

import json
import uuid
from unittest.mock import MagicMock, patch

from workers.janitor import janitor_pass, pending_embed_docs


def _doc(state, total, done):
    doc = MagicMock()
    doc.id = uuid.uuid4()
    doc.state = state
    doc.total_shards = total
    doc.shards_done = done
    doc.shards_failed = 0
    doc.updated_at = None
    return doc


def _entry(doc_id, entry_id="1-1"):
    return (entry_id, {"job": json.dumps({"schema_version": 1, "doc_id": str(doc_id)})})


def _run(docs, stream_entries=None, xrange_raises=None):
    session = MagicMock()
    # janitor_pass calls session.execute twice: first for stuck shards
    # (escalation), then for the docs query. Route by SQL text.
    def execute(stmt, *a, **k):
        res = MagicMock()
        sql = str(stmt)
        if "documents" in sql:
            res.scalars.return_value.all.return_value = docs
        else:
            res.scalars.return_value.all.return_value = []
        return res

    session.execute.side_effect = execute
    redis = MagicMock()
    redis.xinfo_groups.return_value = [
        {"name": "rag-workers", "last-delivered-id": "0-0"}
    ]
    redis.xpending_range.return_value = []
    if xrange_raises is not None:
        redis.xrange.side_effect = xrange_raises
    else:
        redis.xrange.return_value = stream_entries or []
    settings = MagicMock()
    settings.stuck_minutes = 60
    settings.max_shard_attempts = 4  # DLQ cap derivation (R-8) reads this
    with (
        patch("workers.janitor.repo.requeue_expired_leases", return_value=0),
        patch("workers.janitor.repo.book_settled", side_effect=lambda d: (d.shards_done + d.shards_failed) >= d.total_shards),
    ):
        stats = janitor_pass(session, redis, settings)
    return redis, stats


def test_settled_parsing_book_gets_embed_job():
    doc = _doc("parsing", 14, 14)  # all shards done, still parsing
    redis, stats = _run([doc])
    assert redis.xadd.called
    stream = redis.xadd.call_args[0][0]
    assert stream == "doc.embed"
    assert stats["embed_swept"] == 1


def test_unsettled_book_not_enqueued():
    doc = _doc("parsing", 14, 12)  # 12/14 — still parsing shards
    redis, stats = _run([doc])
    assert not redis.xadd.called
    assert stats["embed_swept"] == 0


def test_ready_book_not_reenqueued():
    doc = _doc("ready", 14, 14)  # terminal — sweep must skip
    redis, stats = _run([doc])
    assert not redis.xadd.called
    assert stats["embed_swept"] == 0


def test_mixed_docs_only_settled_parsing_swept():
    settled = _doc("parsing", 9, 9)
    unsettled = _doc("parsing", 30, 10)
    redis, stats = _run([settled, unsettled, _doc("ready", 5, 5), _doc("ready", 3, 3)])
    assert redis.xadd.call_count == 1
    assert stats["embed_swept"] == 1


# --- dedup: a book already holding an undelivered job must be skipped -----


def test_settled_book_with_undelivered_job_not_reenqueued():
    doc = _doc("parsing", 14, 14)
    redis, stats = _run([doc], stream_entries=[_entry(doc.id)])
    assert not redis.xadd.called
    assert stats["embed_swept"] == 0


def test_settled_book_with_job_for_other_doc_still_enqueued():
    doc = _doc("parsing", 14, 14)
    other = uuid.uuid4()
    redis, stats = _run([doc], stream_entries=[_entry(other)])
    assert redis.xadd.call_count == 1
    assert stats["embed_swept"] == 1


def test_scan_failure_skips_sweep_no_blind_reenqueue():
    """If the stream scan fails, skip the sweep entirely — never blind-add
    (that is how the 2026-09-11 flood happened)."""
    doc = _doc("parsing", 14, 14)
    redis, stats = _run([doc], xrange_raises=RuntimeError("redis down"))
    assert not redis.xadd.called
    assert stats["embed_swept"] == 0


def test_pending_embed_docs_scans_all_pages():
    """Pagination: more than one page of entries is fully scanned."""

    class FakeRedis:
        def __init__(self, pages):
            self.pages = pages
            self.calls = []
            self._all = [e for page in pages for e in page]

        def xinfo_groups(self, stream):
            return [{"name": "rag-workers", "last-delivered-id": "0-0"}]

        def xpending_range(self, stream, group, *a):
            return []

        def xrange(self, stream, min="-", max="+", count=500):
            self.calls.append((min, max, count))
            return [
                (eid, f) for eid, f in self._all
                if min == "-" or eid > min[1:]
            ][:count]

    doc_a, doc_b = str(uuid.uuid4()), str(uuid.uuid4())
    page1 = [(f"{i}-0", {"job": json.dumps({"doc_id": doc_a})}) for i in range(1, 501)]
    page2 = [(f"{i}-0", {"job": json.dumps({"doc_id": doc_b})}) for i in range(501, 503)]
    r = FakeRedis([page1, page2])
    ids = pending_embed_docs(r, "doc.embed")
    assert ids == {doc_a, doc_b}
    # second page must start strictly after the last id of page one
    assert r.calls[1][0] == "(500-0"


def test_pending_embed_docs_ignores_malformed_entries():
    class FakeRedis:
        def xinfo_groups(self, stream):
            return [{"name": "rag-workers", "last-delivered-id": "0-0"}]

        def xpending_range(self, stream, group, *a):
            return []

        def xrange(self, stream, min="-", max="+", count=500):
            return [
                ("1-1", {"job": "not-json{"}),
                ("1-2", {"job": json.dumps({"doc_id": str(uuid.uuid4())})}),
                ("1-3", {}),  # no job field
            ]

    r = FakeRedis()
    ids = pending_embed_docs(r, "doc.embed")
    assert len(ids) == 1


def test_pending_embed_docs_excludes_acked_history():
    """Entries up to last-delivered-id are ACKed history (stream is never
    trimmed) — they must NOT suppress future re-enqueues. Only the
    undelivered tail + PEL count."""

    class FakeRedis:
        def __init__(self):
            self.tail = []
            self.pel_entries = []

        def xinfo_groups(self, stream):
            return [{"name": "rag-workers", "last-delivered-id": "100-0"}]

        def xpending_range(self, stream, group, *a):
            return self.pel_entries

        def xrange(self, stream, min="-", max="+", count=500):
            # serve only entries after the exclusive start; PEL lookups by id
            if min.startswith("("):
                lo = min[1:]
                return [(eid, f) for eid, f in self.tail if eid > lo][:count]
            return [(eid, f) for eid, f in self.tail if eid >= min][:count]

    r = FakeRedis()
    acked = str(uuid.uuid4())
    r.history = [(f"100-{i}", {"job": json.dumps({"doc_id": acked})}) for i in range(3)]
    pending = str(uuid.uuid4())
    r.tail = [("101-0", {"job": json.dumps({"doc_id": pending})})]
    ids = pending_embed_docs(r, "doc.embed")
    assert ids == {pending}
    assert acked not in ids


def test_pending_embed_docs_includes_pel_entries():
    class FakeRedis:
        def __init__(self, tail, pel):
            self.tail = tail
            self.pel = pel

        def xinfo_groups(self, stream):
            return [{"name": "rag-workers", "last-delivered-id": "100-0"}]

        def xpending_range(self, stream, group, *a):
            return [{"message_id": m} for m in self.pel]

        def xrange(self, stream, min="-", max="+", count=500):
            if min.startswith("("):
                lo = min[1:]
                return [(eid, f) for eid, f in self.tail if eid > lo][:count]
            return [(eid, f) for eid, f in self.tail if eid >= min][:count]

    r = FakeRedis(
        tail=[("100-0", {"job": json.dumps({"doc_id": str(uuid.uuid4())})})],
        pel=["100-0"],
    )
    # nothing undelivered, but the PEL entry's doc must be in the set
    ids = pending_embed_docs(r, "doc.embed")
    assert len(ids) == 1


def test_pending_embed_docs_no_group_means_empty():
    class FakeRedis:
        def xinfo_groups(self, stream):
            return []

    assert pending_embed_docs(FakeRedis(), "doc.embed") == set()
