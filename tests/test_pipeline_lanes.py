"""/pipeline lane errors (R-20): a dead Redis must not read as an empty
lane silently — the lane dict carries an error, the other lanes stay
populated, and the endpoint does not raise.

2026-09-20 review: the lanes loop `except Exception: pass` made /queues
(errors propagate) and /pipeline (silent empty) disagree on a dead Redis.
"""

from __future__ import annotations

import logging
from unittest.mock import MagicMock

import core.queue.streams as streams_mod
from api.routers.system import pipeline


def _redis(failing_stream: str):
    r = MagicMock()

    def xinfo_groups(stream):
        if stream == failing_stream:
            raise RuntimeError("conn down")
        return [{"name": streams_mod.CONSUMER_GROUP, "last-delivered-id": "0-0"}]

    r.xinfo_groups.side_effect = xinfo_groups
    r.xpending_range.return_value = []
    r.undelivered = None
    return r


def _run(monkeypatch, failing_stream: str, caplog) -> dict:
    monkeypatch.setattr(streams_mod, "make_redis", lambda: _redis(failing_stream))
    # component checks: postgres/redis/qdrant/tei probes are not under test
    import api.routers.system as sys_mod

    monkeypatch.setattr(sys_mod, "_check_postgres", lambda session: "ok")
    monkeypatch.setattr(sys_mod, "_check_redis", lambda: "ok")
    monkeypatch.setattr(sys_mod, "_check_qdrant", lambda url: "ok")
    monkeypatch.setattr(sys_mod, "_check_url", lambda url: "ok")
    monkeypatch.setattr(
        sys_mod, "get_settings", lambda: MagicMock(tei_ingest_url="http://x")
    )
    with caplog.at_level(logging.WARNING):
        return pipeline(session=MagicMock())


def test_pipeline_lane_carries_error_on_dead_redis(monkeypatch, caplog):
    out = _run(monkeypatch, streams_mod.STREAM_PARSE, caplog)
    lane = out["lanes"][streams_mod.STREAM_PARSE]
    assert lane["waiting"] is None  # unknown, not zero
    assert "error" in lane
    assert "conn down" in lane["error"]
    # visible in logs
    assert any("pipeline lane read failed" in r.message for r in caplog.records)


def test_pipeline_other_lanes_still_populated(monkeypatch, caplog):
    out = _run(monkeypatch, streams_mod.STREAM_PARSE, caplog)
    for name in streams_mod.ALL_STREAMS:
        if name == streams_mod.STREAM_PARSE:
            continue
        assert "error" not in out["lanes"][name], name
    # the function did not raise — implicit in reaching here
