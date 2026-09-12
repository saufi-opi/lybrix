"""Tests for /v1/system/queues — must use undelivered_count (NULL-safe lag).

2026-09-12: after mass XDEL cleanup, XINFO GROUPS.lag returns None; the
endpoint's int(lag or 0) collapsed that to 0, so the main dashboard showed
undelivered=0 for doc.parse (real: ~9,165) and doc.embed (real: 2) while
the pipeline page showed true numbers — pages disagreed.
"""

from unittest.mock import MagicMock, patch

import core.queue.streams as streams_mod
from api.routers.system import queues


def _redis(lag, pending=3):
    r = MagicMock()
    r.xpending.return_value = {"pending": pending}
    r.xinfo_groups.return_value = [
        {"name": streams_mod.CONSUMER_GROUP, "lag": lag, "last-delivered-id": "100-0"}
    ]
    return r


def test_queues_uses_fallback_when_lag_null():
    r = _redis(lag=None)
    r.xrange.return_value = [
        ("101-0", {"job": '{"doc_id":"a"}'}),
        ("102-0", {"job": '{"doc_id":"b"}'}),
        ("103-0", {"job": '{"doc_id":"c"}'}),
    ]
    with (
        patch.object(streams_mod, "make_redis", return_value=r),
        patch.object(system_module(), "streams", streams_mod),
    ):
        out = queues()
    assert out["doc.parse"]["undelivered"] == 3
    assert out["doc.parse"]["pending"] == 3
    assert out["doc.parse"]["length"] is not None  # XLEN still reported


def system_module():
    import api.routers.system as s

    return s


def test_queues_fast_path_int_lag():
    r = _redis(lag=9165, pending=12)
    with (
        patch.object(streams_mod, "make_redis", return_value=r),
        patch.object(system_module(), "streams", streams_mod),
    ):
        out = queues()
    assert out["doc.parse"]["undelivered"] == 9165
    assert out["doc.parse"]["pending"] == 12


def test_queues_error_propagates():
    r = _redis(lag=None)
    r.xrange.side_effect = RuntimeError("conn down")
    with (
        patch.object(streams_mod, "make_redis", return_value=r),
        patch.object(system_module(), "streams", streams_mod),
    ):
        import pytest

        with pytest.raises(RuntimeError):
            queues()
