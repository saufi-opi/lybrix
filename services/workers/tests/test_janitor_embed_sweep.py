"""Tests for the janitor settled-book embed sweep (EMBED_RESCUE).

Scenario under test: a parser died between committing the last shard and
XADDing the embed job — book stays state=parsing with all shards done.
The janitor must XADD an embed job for it; docs already ready/partial or
not settled must NOT be re-enqueued.
"""

from __future__ import annotations

import uuid
from unittest.mock import MagicMock, patch

from workers.janitor import janitor_pass


def _doc(state, total, done):
    doc = MagicMock()
    doc.id = uuid.uuid4()
    doc.state = state
    doc.total_shards = total
    doc.shards_done = done
    doc.shards_failed = 0
    doc.updated_at = None
    return doc


def _run(docs):
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
    settings = MagicMock()
    settings.stuck_minutes = 60
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
