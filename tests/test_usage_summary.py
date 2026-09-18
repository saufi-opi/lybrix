"""Usage summary endpoint (Phase 2): fake-session unit tests, no TestClient."""

from __future__ import annotations

import uuid
from datetime import UTC, datetime, timedelta
from types import SimpleNamespace

from api.routers.usage import _cutoff, usage_summary
from core.db.models import ApiKey


class FakeUsageSession:
    """Captures the statement, returns canned aggregate rows."""

    def __init__(self, rows):
        self._rows = rows
        self.captured = None

    def execute(self, stmt, *a, **kw):
        self.captured = stmt
        return SimpleNamespace(all=lambda: self._rows)


def _admin_key():
    return ApiKey(id=uuid.uuid4(), name="admin", key_hash="h" * 64, scopes=["admin"])


def test_summary_totals_and_shape():
    now = datetime.now(UTC)
    k1, k2 = uuid.uuid4(), uuid.uuid4()
    # Plain tuples, matching real SQLAlchemy Row semantics — the router
    # indexes rows positionally (r[0]..r[3]).
    rows = [
        (k1, "ingest key", 12, now - timedelta(minutes=5)),
        (k2, "search key", 3, now - timedelta(hours=2)),
    ]
    out = usage_summary("24h", _admin_key(), FakeUsageSession(rows))
    assert out["period"] == "24h"
    assert out["total"] == 15
    assert out["by_key"][0] == {
        "key_id": str(k1),
        "name": "ingest key",
        "calls": 12,
        "last_used_at": rows[0][3],
    }
    assert [k["key_id"] for k in out["by_key"]] == [str(k1), str(k2)]  # calls desc


def test_summary_empty_period():
    out = usage_summary("30d", _admin_key(), FakeUsageSession([]))
    assert out["total"] == 0
    assert out["by_key"] == []


def test_cutoffs():
    now = datetime(2026, 9, 18, 15, 30, 45, tzinfo=UTC)
    assert _cutoff("today", now) == datetime(2026, 9, 18, 0, 0, tzinfo=UTC)
    assert _cutoff("24h", now) == now - timedelta(hours=24)
    assert _cutoff("7d", now) == now - timedelta(hours=168)
    assert _cutoff("30d", now) == now - timedelta(hours=720)
