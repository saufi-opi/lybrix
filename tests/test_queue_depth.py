"""Tests for queue_depth() — the backpressure counter behind POST /v1/documents.

Regression context (2026-09-11): queue_depth used XLEN + PEL. Streams are
never trimmed, so XLEN counts every entry ever written and only grows; with
8k+ historical entries the depth sat far above MAX_PARSE_BACKLOG and would
have 429'd all new uploads even though the live queue (lag+PEL) was empty.

2026-09-12: lag can be None (untrusted bookkeeping after XDEL) — the depth
must fall back to a live count instead of silently 0, and Redis errors
propagate instead of hiding as an empty queue.
"""

import json
from unittest.mock import MagicMock

import pytest
from core.queue import streams


def _redis(xlen=0, pending=0, groups=None):
    r = MagicMock()
    r.xpending.return_value = {"pending": pending}
    r.xinfo_groups.return_value = (
        groups if groups is not None else [{"name": streams.CONSUMER_GROUP, "lag": 0}]
    )
    r.xlen.return_value = xlen
    return r


def test_depth_uses_group_lag_not_xlen():
    r = _redis(xlen=20_703, pending=23, groups=[{"name": streams.CONSUMER_GROUP, "lag": 11_519}])
    assert streams.queue_depth(r, "doc.parse") == 11_519 + 23


def test_empty_live_queue_returns_zero_despite_history():
    r = _redis(xlen=616, pending=0, groups=[{"name": streams.CONSUMER_GROUP, "lag": 0}])
    assert streams.queue_depth(r, "doc.embed") == 0


def test_missing_group_counts_pending_only():
    r = _redis(xlen=500, pending=7, groups=[])  # group gone → lag unknowable
    assert streams.queue_depth(r, "doc.embed") == 7


def test_redis_error_propagates_from_undelivered():
    """Error policy moved (2026-09-12): swallowing into 0 hid the doc.embed
    lag=NULL incident — undelivered_count now raises and callers decide."""
    r = _redis()
    r.xpending.side_effect = RuntimeError("conn down")
    r.xinfo_groups.side_effect = RuntimeError("conn down")
    with pytest.raises(RuntimeError):
        streams.queue_depth(r, "doc.embed")


def test_none_lag_falls_back_to_live_scan():
    """lag=None (untrusted bookkeeping, e.g. after XDEL) must fall back to
    a live XRANGE count, not silently 0 (2026-09-12 doc.embed showed
    waiting=1 for a 73-job backlog)."""
    r = _redis(xlen=10, pending=2, groups=[{"name": streams.CONSUMER_GROUP, "lag": None}])
    r.xrange.return_value = [
        ("101-0", {"job": json.dumps({"doc_id": "a"})}),
        ("102-0", {"job": json.dumps({"doc_id": "b"})}),
    ]
    assert streams.queue_depth(r, "doc.embed") == 4  # 2 undelivered + 2 PEL
    r.xrange.assert_called_once()
