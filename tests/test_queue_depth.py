"""Tests for queue_depth() — the backpressure counter behind POST /v1/documents.

Regression context (2026-09-11): queue_depth used XLEN + PEL. Streams are
never trimmed, so XLEN counts every entry ever written and only grows; with
8k+ historical entries the depth sat far above MAX_PARSE_BACKLOG and would
have 429'd all new uploads even though the live queue (lag+PEL) was empty.
"""

from unittest.mock import MagicMock

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


def test_redis_error_counts_nothing():
    r = _redis()
    r.xpending.side_effect = RuntimeError("conn down")
    r.xinfo_groups.side_effect = RuntimeError("conn down")
    assert streams.queue_depth(r, "doc.embed") == 0


def test_none_lag_treated_as_zero():
    r = _redis(xlen=10, pending=2, groups=[{"name": streams.CONSUMER_GROUP, "lag": None}])
    assert streams.queue_depth(r, "doc.embed") == 2
