"""Ops sparse backfill script (Phase 1, revised): batching, checkpoint
resume, dry-run, rate limiting, and vector safety (bm25 only, never dense,
never upsert) — fakes only, no network, no live writes.
"""

from __future__ import annotations

import json

import pytest
from qdrant_client import models as qm

from scripts import ops_backfill_sparse as backfill


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


class FakeUpdateVectorsClient:
    """Records update_vectors calls; count() mimics the final verification."""

    def __init__(self):
        self.update_calls = []
        self.total = 23

    def update_vectors(self, collection_name, points, wait):
        self.update_calls.append(
            {"collection_name": collection_name, "points": points, "wait": wait}
        )

    def count(self, collection_name, count_filter=None, exact=True):
        class R:
            count = self.total if count_filter is None else self.total

        return R()


class FakeDryRunClient:
    """A client whose update_vectors must never be called (dry-run assertion)."""

    def update_vectors(self, *a, **kw):
        raise AssertionError("dry-run must not write to Qdrant")


@pytest.fixture
def rows():
    """Deterministic rows: hashes ordered so batching/resume boundaries are
    stable (chunk_hash ordering is lexicographic)."""
    return [(f"{i:064d}", f"text-{i}") for i in range(23)]


def test_batching_points_per_call_and_vector_shape(rows):
    client = FakeUpdateVectorsClient()
    backfill.run_backfill(
        client, FakeSessionFactory(rows), batch=10, checkpoint_path=None
    )
    # 23 rows at batch=10 -> 3 update_vectors calls (10, 10, 3)
    assert [len(c["points"]) for c in client.update_calls] == [10, 10, 3]
    # every point carries ONLY the bm25 sparse vector — never dense
    for call in client.update_calls:
        for pv in call["points"]:
            assert isinstance(pv, qm.PointVectors)
            assert set(pv.vector) == {"bm25"}
            assert isinstance(pv.vector["bm25"], qm.SparseVector)
        assert call["wait"] is False
    # point ids map back to the deterministic uuid5 of chunk_hash
    assert client.update_calls[0]["points"][0].id == backfill.point_id_for(rows[0][0])
    assert client.update_calls[-1]["points"][-1].id == backfill.point_id_for(rows[-1][0])


def test_update_vectors_values_come_from_encoder(rows):
    client = FakeUpdateVectorsClient()
    backfill.run_backfill(
        client, FakeSessionFactory(rows[:3]), batch=10, checkpoint_path=None
    )
    sv = client.update_calls[0]["points"][1].vector["bm25"]
    expected = backfill.encode_bm25("text-1")
    assert sv.indices == expected["indices"]
    assert sv.values == expected["values"]


def test_checkpoint_resume_skips_prefix(rows, tmp_path):
    cp = tmp_path / "cp.json"
    cp.write_text(json.dumps({"last_chunk_hash": rows[9][0], "done": 10, "total": 23}))
    client = FakeUpdateVectorsClient()
    backfill.run_backfill(
        client, FakeSessionFactory(rows), batch=10, checkpoint_path=cp
    )
    written = [pv.id for call in client.update_calls for pv in call["points"]]
    assert len(written) == 13  # rows 10..22 only
    assert written[0] == backfill.point_id_for(rows[10][0])


def test_checkpoint_resume_within_batch(rows, tmp_path):
    """A batch interrupted mid-way: checkpoint hash inside batch 2 (after 12
    rows) -> only rows 13..22 are written."""
    cp = tmp_path / "cp.json"
    cp.write_text(json.dumps({"last_chunk_hash": rows[11][0], "done": 12, "total": 23}))
    client = FakeUpdateVectorsClient()
    backfill.run_backfill(
        client, FakeSessionFactory(rows), batch=10, checkpoint_path=cp
    )
    written = [pv.id for call in client.update_calls for pv in call["points"]]
    assert len(written) == 11
    assert written[0] == backfill.point_id_for(rows[12][0])


def test_dry_run_performs_zero_writes(rows, capsys):
    client = FakeDryRunClient()
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
    client = FakeUpdateVectorsClient()
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
    client = FakeUpdateVectorsClient()
    backfill.run_backfill(
        client, FakeSessionFactory(rows), batch=10, rate=1.0, checkpoint_path=None
    )
    assert sleeps == []  # floor already met: no sleeps


def test_fatal_error_returns_1(rows):
    class BoomClient:
        def update_vectors(self, **kw):
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
