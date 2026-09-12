"""Tests for trim-on-ACK: processed jobs must not live in the stream forever.

2026-09-12 incident: doc.parse grew to 25,073 entries for 15,994 real
shards — 36% of the stream was stale duplicates (janitor reclaim kept
re-adding ACKed PEL entries), and parsers burned time reading them.
Contract:

- ``streams.ack()`` XACKs and then XDELs the entry (best-effort: a failed
  XDEL must never fail the job that just completed).
- Unparseable jobs in ``read_jobs`` are ACK+XDEL too.
- Janitor reclaim ACKs the old entry after re-adding → old entry trimmed.
"""

from __future__ import annotations

import json
from unittest.mock import MagicMock

from core.queue import streams


class RecordingRedis:
    def __init__(self, xdel_raises=None):
        self.calls = []
        self._xdel_raises = xdel_raises

    def xack(self, stream, group, entry_id):
        self.calls.append(("xack", stream, group, entry_id))
        return 1

    def xdel(self, stream, *entry_ids):
        if self._xdel_raises is not None:
            raise self._xdel_raises
        self.calls.append(("xdel", stream, entry_ids))
        return len(entry_ids)


def test_ack_deletes_entry_after_xack():
    r = RecordingRedis()
    streams.ack(r, "doc.embed", "123-0")
    assert ("xack", "doc.embed", streams.CONSUMER_GROUP, "123-0") in r.calls
    assert ("xdel", "doc.embed", ("123-0",)) in r.calls
    # xack must happen before xdel
    order = [c[0] for c in r.calls]
    assert order.index("xack") < order.index("xdel")


def test_ack_xdel_failure_is_best_effort():
    r = RecordingRedis(xdel_raises=RuntimeError("redis busy"))
    streams.ack(r, "doc.parse", "5-1")  # must not raise
    assert ("xack", "doc.parse", streams.CONSUMER_GROUP, "5-1") in r.calls


def test_read_jobs_unparseable_job_acked_and_deleted():
    r = MagicMock()
    r.xreadgroup.return_value = [
        ("doc.parse", [("9-9", {"job": "{not json"})]),
    ]
    out = streams.read_jobs(r, "doc.parse", "c1", count=1, block_ms=0)
    assert out == []
    r.xack.assert_called_once()
    r.xdel.assert_called_once_with("doc.parse", "9-9")


def test_read_jobs_good_job_not_deleted_on_read():
    r = MagicMock()
    r.xreadgroup.return_value = [
        ("doc.parse", [("9-9", {"job": json.dumps({"doc_id": "x"})})]),
    ]
    out = streams.read_jobs(r, "doc.parse", "c1", count=1, block_ms=0)
    assert len(out) == 1
    r.xdel.assert_not_called()  # trim happens on ACK, not on delivery


def test_janitor_reclaim_trims_old_entry():
    from workers.janitor import janitor_pass

    old_entry = ("42-0", {"job": json.dumps({"schema_version": 1, "doc_id": "d"})})
    session = MagicMock()

    def execute(stmt, *a, **k):
        res = MagicMock()
        res.scalars.return_value.all.return_value = []
        return res

    session.execute.side_effect = execute
    redis = MagicMock()
    # only doc.parse yields a reclaimed entry; other streams empty
    redis.xautoclaim.side_effect = lambda stream, *a, **k: (
        ("0-0", [old_entry], "0-0") if stream == "doc.parse" else ("0-0", [], "0-0")
    )
    settings = MagicMock()
    settings.stuck_minutes = 60
    settings.max_shard_attempts = 4  # DLQ cap derivation (R-8) reads this
    with (
        __import__("unittest.mock", fromlist=["patch"]).patch(
            "workers.janitor.repo.requeue_expired_leases", return_value=0
        ),
    ):
        stats = janitor_pass(session, redis, settings)
    assert stats["reclaimed"] == 1
    # old entry acked AND deleted; fresh re-add present
    redis.xack.assert_called_once()
    redis.xdel.assert_called_once()
    assert redis.xadd.called
