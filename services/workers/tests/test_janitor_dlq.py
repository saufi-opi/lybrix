"""Janitor reclaim delivery cap → DLQ quarantine (R-8).

The runner leaves failed jobs unacked in the PEL, so the janitor reclaim
(XAUTOCLAIM + re-add) revisits them forever — and the re-add resets the
entry, so a job whose handler always raises (vanished doc → KeyError in
the parser) loops deliver→raise→reclaim unbounded. Contract locked here:

- an entry whose times_delivered is at/over the cap
  (max(5, max_shard_attempts + 1)) is NOT re-added: quarantine() writes
  one DLQ events row and drops the entry (XACK + XDEL)
- an entry under the cap re-adds exactly as before (no quarantine)
- the cap read is fail-open: an XPENDING failure or an empty answer
  means re-add (never quarantine on a guess)
- a malformed job payload still quarantines when over cap — the
  doc_id/idx parse failure must not crash the janitor pass
"""

from __future__ import annotations

import json
import uuid
from unittest.mock import MagicMock, patch

from workers.janitor import janitor_pass

CAP = 5  # max(5, max_shard_attempts=4 + 1)


def _job(**extra):
    return json.dumps({"schema_version": 1, "doc_id": str(uuid.uuid4()), **extra})


def _base_redis():
    redis = MagicMock()
    redis.xautoclaim.side_effect = lambda stream, *a, **k: ("0-0", [], "0-0")
    redis.xinfo_groups.return_value = [{"name": "rag-workers", "last-delivered-id": "0-0"}]
    redis.xrange.return_value = []
    return redis


def _settings(max_attempts=4):
    settings = MagicMock()
    settings.max_shard_attempts = max_attempts
    settings.stuck_minutes = 60
    return settings


def _session(docs=()):
    def execute(stmt, *a, **k):
        res = MagicMock()
        res.scalars.return_value.all.return_value = list(docs)
        return res

    session = MagicMock()
    session.execute.side_effect = execute
    return session


def _redis_with_reclaim_entries(entries, times_delivered):
    """Redis whose doc.parse PEL reports ``times_delivered`` for every
    reclaimed entry; keyword-form xpending_range (the cap read) answers
    per entry, positional form (the dedup PEL scan) stays empty."""
    redis = _base_redis()
    redis.xautoclaim.side_effect = lambda stream, *a, **k: (
        ("0-0", list(entries), "0-0") if stream == "doc.parse" else ("0-0", [], "0-0")
    )

    def xpending_range(stream, group, *a, **k):
        if k.get("min") is not None:  # cap read: min=entry_id, max=entry_id
            return [{"message_id": k["min"], "times_delivered": times_delivered}]
        return []  # positional "-"/"+" scan from pending_job_doc_ids

    redis.xpending_range.side_effect = xpending_range
    return redis


def _run_pass(session, redis, settings):
    with (
        patch("workers.janitor.repo.requeue_expired_leases", return_value=0),
        patch("workers.janitor.repo.book_settled", return_value=False),
        patch("workers.janitor.write_evt"),
        patch("workers.janitor.streams.quarantine") as quarantine,
    ):
        stats = janitor_pass(session, redis, settings)
    return stats, quarantine


def test_entry_over_cap_quarantined_not_readded():
    """At/over the cap → DLQ row, no re-add, entry ACKed and deleted."""
    entries = [(f"42-{i}", {"job": _job()}) for i in range(2)]
    redis = _redis_with_reclaim_entries(entries, times_delivered=CAP)
    session = _session()

    stats, quarantine = _run_pass(session, redis, _settings())

    assert stats["reclaimed"] == 0
    assert stats["quarantined"] == 2
    assert quarantine.call_count == 2
    # the janitor's own PG session is passed through (no new session opened)
    assert quarantine.call_args[0][0] is session
    # quarantined entry is dropped from the stream, never re-added
    assert not redis.xadd.called
    for call in quarantine.call_args_list:
        _session_arg, _r, stream, entry_id, raw, delivered = call.args
        assert stream == "doc.parse"
        assert delivered == CAP
        assert json.loads(raw)["doc_id"]


def test_entry_under_cap_readded_no_quarantine():
    """Under the cap → re-add (old reclaim behavior), no DLQ row."""
    entries = [("42-0", {"job": _job()})]
    redis = _redis_with_reclaim_entries(entries, times_delivered=CAP - 1)

    stats, quarantine = _run_pass(_session(), redis, _settings())

    assert stats["reclaimed"] == 1
    assert stats["quarantined"] == 0
    assert not quarantine.called
    assert redis.xadd.call_count == 1
    assert redis.xack.call_count == 1
    assert redis.xdel.call_count == 1


def test_cap_derived_from_max_shard_attempts():
    """cap = max(5, max_shard_attempts + 1): with attempts=8 the cap is 9,
    so 5 deliveries still re-adds."""
    entries = [("42-0", {"job": _job()})]
    redis = _redis_with_reclaim_entries(entries, times_delivered=5)

    stats, quarantine = _run_pass(_session(), redis, _settings(max_attempts=8))

    assert stats["reclaimed"] == 1
    assert stats["quarantined"] == 0
    assert not quarantine.called


def test_xpending_failure_fails_open_readd():
    """XPENDING blowing up must not quarantine OR crash the pass: the
    entry re-adds (pre-R-8 behavior) and the janitor moves on."""
    entries = [("42-0", {"job": _job()})]
    redis = _base_redis()
    redis.xautoclaim.side_effect = lambda stream, *a, **k: (
        ("0-0", list(entries), "0-0") if stream == "doc.parse" else ("0-0", [], "0-0")
    )
    redis.xpending_range.side_effect = RuntimeError("redis timeout")

    stats, quarantine = _run_pass(_session(), redis, _settings())

    assert stats["reclaimed"] == 1
    assert stats["quarantined"] == 0
    assert not quarantine.called
    assert redis.xadd.call_count == 1


def test_empty_pending_answer_fails_open_readd():
    """An empty XPENDING answer (entry already gone) also fails open."""
    entries = [("42-0", {"job": _job()})]
    redis = _base_redis()
    redis.xautoclaim.side_effect = lambda stream, *a, **k: (
        ("0-0", list(entries), "0-0") if stream == "doc.parse" else ("0-0", [], "0-0")
    )
    redis.xpending_range.return_value = []

    stats, quarantine = _run_pass(_session(), redis, _settings())

    assert stats["reclaimed"] == 1
    assert not quarantine.called


def test_malformed_job_json_still_quarantined_over_cap():
    """A husk with unparseable JSON must still be quarantined when over
    cap — the doc_id/idx parse failure must not crash the janitor (and
    must not leave the poison entry looping)."""
    entries = [("42-0", {"job": "{not json"})]
    redis = _redis_with_reclaim_entries(entries, times_delivered=CAP)

    stats, quarantine = _run_pass(_session(), redis, _settings())

    assert stats["quarantined"] == 1
    assert stats["reclaimed"] == 0
    assert quarantine.call_args[0][4] == "{not json"  # raw job passed through


# --- streams.quarantine unit behavior ---


def test_quarantine_writes_dlq_event_and_drops_entry():
    from core.queue import streams

    session = MagicMock()
    redis = MagicMock()
    raw = _job(idx=3)
    doc_id = json.loads(raw)["doc_id"]

    with patch("core.events.write_event") as write_event:
        streams.quarantine(session, redis, "doc.parse", "42-1", raw, 5)

    assert write_event.call_count == 1
    _s, level, stage, message = write_event.call_args[0]
    kwargs = write_event.call_args[1]
    assert (level, stage, kwargs["code"]) == ("error", "dlq", "DLQ")
    assert "doc.parse" in message and "5 deliveries" in message
    assert str(kwargs["doc_id"]) == doc_id
    assert kwargs["shard_idx"] == 3
    assert kwargs["context"]["detail"] == raw
    # the event is written into the CALLER's session (write_event's first
    # positional arg) — quarantine never opens its own
    assert write_event.call_args[0][0] is session
    redis.xack.assert_called_once_with("doc.parse", streams.CONSUMER_GROUP, "42-1")
    redis.xdel.assert_called_once_with("doc.parse", "42-1")


def test_quarantine_malformed_json_no_doc_link_still_quarantines():
    from core.queue import streams

    session = MagicMock()
    redis = MagicMock()

    with patch("core.events.write_event") as write_event:
        streams.quarantine(session, redis, "doc.embed", "7-2", "{oops", 9)

    kwargs = write_event.call_args[1]
    assert kwargs["doc_id"] is None
    assert kwargs["shard_idx"] is None
    assert kwargs["context"]["detail"] == "{oops"
    redis.xack.assert_called_once()
    redis.xdel.assert_called_once()


def test_quarantine_redis_failures_never_raise():
    """XACK/XDEL are best-effort: a Redis blip must not fail the janitor
    pass AFTER the DLQ row was written (the events row is the record)."""
    from core.queue import streams

    session = MagicMock()
    redis = MagicMock()
    redis.xack.side_effect = RuntimeError("redis down")
    redis.xdel.side_effect = RuntimeError("redis down")

    with patch("core.events.write_event"):
        streams.quarantine(session, redis, "doc.parse", "42-1", _job(), 6)  # no raise

    assert redis.xack.called and redis.xdel.called
