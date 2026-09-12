"""Janitor queue-hygiene invariants (locks the 2026-09-11/12 incidents).

Contract locked here:
- shards with attempts >= max_shard_attempts are escalated to failed with
  an error event (parse has a terminal guard — retries are bounded)
- escalation only touches max-attempt shards
- janitor reclaim NEVER grows a stream: every re-add is paired with an
  XACK+XDEL of the old entry (2026-09-12 "36% dupe stream" incident)
- the doc.split requeue XADDs once per UPLOADED doc per pass — documents
  the current behavior and the known R-6 flood gap: there is no
  undelivered-dedup on this requeue (same bug class as the 2026-09-11
  embed flood), so a UPLOADED doc whose split job sits unacked is
  re-XADDed EVERY pass. BACKLOG R-6 tracks the fix; the dedup test lands
  with it.
"""

from __future__ import annotations

import json
import uuid
from unittest.mock import MagicMock, patch

from workers.janitor import janitor_pass


def _doc(state):
    doc = MagicMock()
    doc.id = uuid.uuid4()
    doc.state = state
    doc.source_uri = f"s3://raw/{doc.id}.pdf"
    doc.updated_at = None
    doc.total_shards = 4
    doc.shards_done = 0
    doc.shards_failed = 0
    return doc


def _shard(attempts, idx=0):
    shard = MagicMock()
    shard.idx = idx
    shard.attempts = attempts
    shard.doc_id = uuid.uuid4()
    return shard


def _base_redis():
    redis = MagicMock()
    redis.xautoclaim.side_effect = lambda stream, *a, **k: ("0-0", [], "0-0")
    redis.xinfo_groups.return_value = [{"name": "rag-workers", "last-delivered-id": "0-0"}]
    redis.xpending_range.return_value = []
    redis.xrange.return_value = []
    return redis


def _base_settings(max_attempts=4):
    settings = MagicMock()
    settings.max_shard_attempts = max_attempts
    settings.stuck_minutes = 60
    return settings


def _base_session(docs=(), stuck_shards=()):
    def execute(stmt, *a, **k):
        res = MagicMock()
        sql = str(stmt)
        if "FROM shards" in sql:
            res.scalars.return_value.all.return_value = list(stuck_shards)
        else:
            res.scalars.return_value.all.return_value = list(docs)
        return res

    session = MagicMock()
    session.execute.side_effect = execute
    return session


def _run_pass(session, redis, settings):
    captured = {"events": []}

    def fake_write_evt(_session, **kw):
        captured["events"].append(kw)

    with (
        patch("workers.janitor.repo.requeue_expired_leases", return_value=0),
        patch("workers.janitor.repo.book_settled", return_value=False),
        patch("workers.janitor.write_evt", side_effect=fake_write_evt),
    ):
        stats = janitor_pass(session, redis, settings)
    return stats, captured


def test_escalated_shard_marked_failed_with_event():
    shard = _shard(attempts=4)
    shard.state = "pending"
    stats, captured = _run_pass(
        _base_session(stuck_shards=[shard]), _base_redis(), _base_settings(max_attempts=4)
    )

    assert stats["escalated"] == 1
    assert shard.state == "failed"
    assert shard.error_code == "SHARD_TIMEOUT"
    # an error event names the shard and the escalation
    evt = next(e for e in captured["events"] if e.get("code") == "SHARD_TIMEOUT")
    assert evt["level"] == "error"
    assert evt["stage"] == "parse"
    assert evt["doc_id"] == shard.doc_id
    assert evt["shard_idx"] == shard.idx


def test_escalation_only_touches_max_attempt_shards():
    # the janitor query itself filters attempts >= max; a below-cap shard
    # never enters the escalation loop
    low = _shard(attempts=2)

    def execute(stmt, *a, **k):
        res = MagicMock()
        sql = str(stmt)
        if "FROM shards" in sql:
            # verify the filter is IN the query, not just in the fixture
            assert "shards.attempts >=" in sql
            res.scalars.return_value.all.return_value = []
        else:
            res.scalars.return_value.all.return_value = []
        return res

    session = MagicMock()
    session.execute.side_effect = execute

    stats, _ = _run_pass(session, _base_redis(), _base_settings(max_attempts=4))

    assert stats["escalated"] == 0
    assert low.attempts == 2  # untouched


def test_reclaim_never_grows_stream():
    """K reclaimed entries → every xadd is matched by an xdel of the OLD
    entry (xack + xdel). Net stream growth must be <= 0: reclaim re-adds
    a fresh copy and trims the old entry in the same pass (2026-09-12
    incident: re-add alone grew doc.parse to 36% dupes)."""
    K = 3
    entries = [
        (f"42-{i}", {"job": json.dumps({"schema_version": 1, "doc_id": str(uuid.uuid4())})})
        for i in range(K)
    ]
    redis = _base_redis()
    # only doc.parse yields reclaimed entries; the other streams are empty
    redis.xautoclaim.side_effect = lambda stream, *a, **k: (
        ("0-0", list(entries), "0-0") if stream == "doc.parse" else ("0-0", [], "0-0")
    )

    stats, _ = _run_pass(_base_session(), redis, _base_settings())

    assert stats["reclaimed"] == K
    # one xadd per reclaimed entry; one xack AND one xdel per entry
    assert redis.xadd.call_count == K
    assert redis.xack.call_count == K
    assert redis.xdel.call_count == K
    # every xdel targets the OLD entry ids — nothing stays untrimmed
    deleted: set[str] = set()
    for call in redis.xdel.call_args_list:
        deleted.update(call.args[1:])
    assert deleted == {eid for eid, _ in entries}
    # net XLEN delta: K removed, K added → 0 (never positive)
    assert redis.xdel.call_count - redis.xadd.call_count == 0


def test_requeue_uploaded_doc_xadds_once_per_pass():
    """Current behavior: one doc.split XADD per UPLOADED doc per pass.

    KNOWN GAP (BACKLOG R-6, not fixed here): there is no
    undelivered-dedup on this requeue — a UPLOADED doc whose split job
    sits unacked is re-XADDed EVERY pass, the same bug class as the
    2026-09-11 doc.embed flood. The fix must add a pending_embed_docs-
    style dedup scan for doc.split; this test locks the per-pass
    invariant the fix has to preserve.
    """
    doc = _doc("uploaded")
    redis = _base_redis()
    stats, _ = _run_pass(_base_session(docs=[doc]), redis, _base_settings())

    assert stats["reenqueued"] == 1
    assert redis.xadd.call_count == 1  # exactly one job per pass
    stream = redis.xadd.call_args[0][0]
    assert stream == "doc.split"
    job_payload = json.loads(redis.xadd.call_args[0][1]["job"])
    assert job_payload["doc_id"] == str(doc.id)
