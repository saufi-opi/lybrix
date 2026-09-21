"""Metrics rollup writer (Phase 6, PRD §10.1) + R-18 embedded_at stamp.

write_metrics_rollup: one metrics_rollup row per minute bucket, windowed
p50/p95 from shards.done_at, per-minute deltas from the previous bucket's
snapshot. Fake session capturing the merged object + canned shards.
"""

from __future__ import annotations

import uuid
from datetime import UTC, datetime, timedelta
from unittest.mock import MagicMock, patch

import workers.janitor as janitor_mod
from workers.janitor import write_metrics_rollup


def _shard(duration_ms, page_start=1, page_end=21, state="done", peak_rss_mb=None):
    shard = MagicMock()
    shard.duration_ms = duration_ms
    shard.page_start = page_start
    shard.page_end = page_end
    shard.state = state
    shard.peak_rss_mb = peak_rss_mb
    shard.done_at = None
    shard.idx = 0
    shard.doc_id = uuid.uuid4()
    return shard


class FakeSession:
    """Captures merge()ed objects; execute() returns the canned shards."""

    def __init__(self, shards):
        self._shards = shards
        self.merged: list = []

    def execute(self, stmt, *a, **kw):
        return MagicMock(
            scalars=lambda: MagicMock(all=lambda: list(self._shards)),
            scalar_one=lambda: 3,
        )

    def merge(self, obj):
        self.merged.append(obj)


def _window_settings():
    return MagicMock()


def _redis():
    r = MagicMock()
    r.xpending.return_value = {"pending": 0}
    r.xinfo_groups.return_value = [{"name": "rag-workers", "last-delivered-id": "0-0"}]
    r.xrange.return_value = []
    return r


def _clean(monkeypatch):
    """Reset the module-level _last_bucket between tests."""
    monkeypatch.setattr(janitor_mod, "_last_bucket", None)


def test_rollup_pages_p50_p95_and_bucket_floor(monkeypatch):
    _clean(monkeypatch)
    now = datetime.now(UTC).replace(second=0, microsecond=0)
    # shards with durations 100..500 ms in the window
    shards = [_shard(d) for d in (100, 200, 300, 400, 500)]
    session = FakeSession(shards)
    redis = _redis()

    # pin 'now' so the bucket math is deterministic
    class FakeDT(datetime):
        @classmethod
        def now(cls, tz=None):
            return now + timedelta(minutes=1)

    with patch.object(janitor_mod, "datetime", FakeDT):
        wrote = write_metrics_rollup(session, redis, _window_settings())
    assert wrote is True
    (rollup,) = session.merged
    # bucket floors to the minute
    assert rollup.bucket == now + timedelta(minutes=1)
    assert rollup.pages_parsed == 5 * 21
    assert rollup.shards_done == 5
    assert rollup.shards_failed == 0
    # p50/p95 by index over the sorted durations
    assert rollup.parse_p50_ms == 300
    assert rollup.parse_p95_ms == 500
    # peak_rss recorded as 0 → pct returns the sorted index value 0, not None
    assert rollup.peak_rss_p95_mb == 0
    assert rollup.chunks_embedded == 3
    assert rollup.search_p95_ms is None and rollup.search_count is None


def test_rollup_second_call_same_minute_noop(monkeypatch):
    _clean(monkeypatch)
    now = datetime.now(UTC).replace(second=0, microsecond=0)
    shards = [_shard(100)]
    session = FakeSession(shards)
    redis = _redis()

    class FakeDT(datetime):
        @classmethod
        def now(cls, tz=None):
            return now + timedelta(minutes=1)

    with patch.object(janitor_mod, "datetime", FakeDT):
        assert write_metrics_rollup(session, redis, _window_settings()) is True
        assert len(session.merged) == 1
        # same bucket, no new rows since: no-op (a re-merge with rows would
        # also be safe — idempotent upsert on the bucket PK — but the no-op
        # path saves the query)
        session._shards = []
        assert write_metrics_rollup(session, redis, _window_settings()) is False
        assert len(session.merged) == 1


def test_rollup_queue_depth_read_failure_never_raises(monkeypatch):
    _clean(monkeypatch)
    shards = [_shard(100)]
    session = FakeSession(shards)
    redis = _redis()
    redis.xpending.side_effect = RuntimeError("redis down")

    with patch.object(
        janitor_mod.streams, "queue_depth", side_effect=RuntimeError("redis down")
    ):
        wrote = write_metrics_rollup(session, redis, _window_settings())
    assert wrote is True
    (rollup,) = session.merged
    assert rollup.queue_depth == {}  # degraded, not aborted


def test_mark_shard_done_stamps_done_at():
    """repo.mark_shard_done sets shards.done_at — the rollup's window key."""
    import inspect

    from core.db import repo

    src = inspect.getsource(repo.mark_shard_done)
    assert "done_at" in src


def test_embedder_insert_carries_embedded_at():
    """R-18: the embedder's chunk insert stamps chunks.embedded_at."""
    import inspect

    from workers import embedder as embedder_mod

    src = inspect.getsource(embedder_mod.handle_embed)
    assert "embedded_at" in src
