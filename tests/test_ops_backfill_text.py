"""Ops backfill script (Phase 1): batching, checkpoint resume, dry-run,
and rate limiting — fakes only, no network, no live writes.
"""

from __future__ import annotations

import json

import pytest

from scripts import ops_backfill_text as backfill


class FakeResult:
    def __init__(self, rows):
        self._rows = rows

    def yield_per(self, n):
        return iter(self._rows)  # yield_per is transparent to the batching shim


class FakeSession:
    def __init__(self, rows):
        self._rows = rows

    def execute(self, stmt):
        return FakeResult(self._rows)

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False


class FakeSessionFactory:
    def __init__(self, rows):
        self._rows = rows

    def __call__(self):
        return FakeSession(self._rows)


class FakeSetPayloadClient:
    """Records set_payload calls; count() mimics the final verification."""

    def __init__(self):
        self.payload_calls = []
        self.count_result = 323070

    def set_payload(self, collection_name, payload, points, wait):
        self.payload_calls.append(
            {"collection_name": collection_name, "payload": payload, "points": points,
             "wait": wait}
        )

    def count(self, collection_name, exact):
        assert exact is True
        class R:
            count = self.count_result
        return R()


class FakeCountOnlyClient:
    """A client whose set_payload must never be called (dry-run assertion)."""

    def set_payload(self, *a, **kw):
        raise AssertionError("dry-run must not write to Qdrant")


@pytest.fixture
def rows():
    """Deterministic rows: hashes ordered so batching/resume boundaries are
    stable (chunk_hash ordering is lexicographic)."""
    return [(f"{i:064d}", f"text-{i}") for i in range(23)]


def test_batching_batches_and_point_ids(rows):
    client = FakeSetPayloadClient()
    backfill.run_backfill(
        client, FakeSessionFactory(rows), batch=10, checkpoint_path=None
    )
    assert len(client.payload_calls) == 23  # one call per unique text
    # batch boundaries: hashes 0-9, 10-19, 20-22 in call order
    first_hashes = [c["points"][0] for c in client.payload_calls]
    assert first_hashes[0] == backfill.point_id_for(rows[0][0])
    assert first_hashes[-1] == backfill.point_id_for(rows[-1][0])
    # set_payload never touches vectors: payload only carries "text"
    assert all(set(c["payload"]) == {"text"} for c in client.payload_calls)
    assert all(c["wait"] is False for c in client.payload_calls)


def test_checkpoint_resume_skips_prefix(rows, tmp_path):
    cp = tmp_path / "cp.json"
    # checkpoint says the first 10 rows (hash <= rows[9][0]) are done
    cp.write_text(json.dumps({"last_chunk_hash": rows[9][0], "done": 10, "total": 23}))
    client = FakeSetPayloadClient()
    backfill.run_backfill(
        client, FakeSessionFactory(rows), batch=10, checkpoint_path=cp
    )
    written = [c["points"][0] for c in client.payload_calls]
    assert len(written) == 13  # rows 10..22 only
    assert written[0] == backfill.point_id_for(rows[10][0])


def test_checkpoint_resume_within_batch(rows, tmp_path):
    """A batch interrupted mid-way: checkpoint hash inside batch 2 (after 12
    rows) -> only rows 13..22 are written."""
    cp = tmp_path / "cp.json"
    cp.write_text(json.dumps({"last_chunk_hash": rows[11][0], "done": 12, "total": 23}))
    client = FakeSetPayloadClient()
    backfill.run_backfill(
        client, FakeSessionFactory(rows), batch=10, checkpoint_path=cp
    )
    assert len(client.payload_calls) == 11
    assert client.payload_calls[0]["points"][0] == backfill.point_id_for(rows[12][0])


def test_dry_run_performs_zero_writes(rows, capsys):
    client = FakeCountOnlyClient()
    exit_code = backfill.run_backfill(
        client, FakeSessionFactory(rows), batch=10, checkpoint_path=None, dry_run=True
    )
    assert exit_code == 0
    out = capsys.readouterr().out
    assert "dry-run: 23 chunks" in out
    assert "3 batches" in out
    assert "zero Qdrant writes" in out


def test_rate_limiting_sleeps_as_expected(rows, monkeypatch):
    sleeps = []

    class FakeTime:
        now = 1000.0

        @classmethod
        def monotonic(cls):
            return cls.now

        @classmethod
        def sleep(cls, seconds):
            sleeps.append(seconds)
            cls.now += seconds  # sleeping advances the clock

    monkeypatch.setattr(backfill.time, "monotonic", FakeTime.monotonic)
    monkeypatch.setattr(backfill.time, "sleep", FakeTime.sleep)
    client = FakeSetPayloadClient()
    backfill.run_backfill(
        client, FakeSessionFactory(rows), batch=10, rate=10.0, checkpoint_path=None
    )
    # Batches 1-2 have 10 rows -> floor 1.0s each; the fake clock never
    # advances from writes, so each full batch sleeps exactly its floor.
    # Batch 3 has 3 rows -> floor 0.3s.
    assert sleeps == pytest.approx([1.0, 1.0, 0.3])


def test_rate_limiting_no_sleep_when_already_slow(rows, monkeypatch):
    """When real throughput is slower than the limit (elapsed >= floor), no
    sleep is added: 23 rows at rate=1/s -> floor 10s per full batch; each
    monotonic() read advances the fake clock 12s, so elapsed always covers
    the floor."""
    sleeps = []

    class SlowClock:
        t = 1000.0

        @classmethod
        def monotonic(cls):
            cls.t += 12.0
            return cls.t

    monkeypatch.setattr(backfill.time, "sleep", lambda s: sleeps.append(s))
    monkeypatch.setattr(backfill.time, "monotonic", SlowClock.monotonic)
    client = FakeSetPayloadClient()
    backfill.run_backfill(
        client, FakeSessionFactory(rows), batch=10, rate=1.0, checkpoint_path=None
    )
    assert sleeps == []  # floor already met: no sleeps


def test_fatal_error_returns_1(rows, tmp_path):
    class BoomClient:
        def set_payload(self, **kw):
            raise RuntimeError("qdrant down")

    exit_code = backfill.run_backfill(
        BoomClient(), FakeSessionFactory(rows), batch=10, checkpoint_path=None
    )
    assert exit_code == 1


def test_batched_shape():
    got = list(backfill.batched(iter(range(7)), 3))
    assert got == [[0, 1, 2], [3, 4, 5], [6]]


def test_load_checkpoint_missing_or_bad(tmp_path):
    assert backfill.load_checkpoint(tmp_path / "absent.json") is None
    bad = tmp_path / "bad.json"
    bad.write_text("not json")
    assert backfill.load_checkpoint(bad) is None
