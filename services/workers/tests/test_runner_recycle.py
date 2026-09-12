"""Tests for PARSER_RECYCLE_AFTER — job-count-based process recycle (R-7).

Compose sets PARSER_RECYCLE_AFTER=10 (and the parser docstring promises
it) but nothing implemented it: parser processes lived for days and RSS
grew unbounded (SoftOOM was the only backstop). Contract:

- run_consumer counts successfully handled jobs; at recycle_after it
  returns cleanly (docker's restart: unless-stopped revives the process;
  the in-flight job was already ACKed, nothing is lost).
- recycle_after=0 disables the feature (consume forever).
- exiting happens only on a job boundary — a failing job still runs its
  error path and the loop continues past it (the count advances on
  success only).
"""

from __future__ import annotations

from unittest.mock import MagicMock, patch

from workers.runner import run_consumer


def _run(jobs, recycle_after, *, handler_side_effect=None):
    """Drive run_consumer over a fixed job list; return handled jobs.

    read_jobs hands out the pool one job at a time; when exhausted it
    returns [] forever. With recycle disabled that means the loop would
    poll forever — the test fails on the sleep-raises sentinel instead,
    proving the exit happened.
    """
    handled = []
    pool = list(jobs)

    def fake_read(r, stream, consumer, count=1, block_ms=5000):
        return [(f"{len(pool)}-0", pool.pop(0))] if pool else []

    def handler(session, job):
        if handler_side_effect is not None:
            handler_side_effect(job)
        handled.append(job)

    session_factory = MagicMock()
    session_factory.return_value.__enter__ = MagicMock(return_value=MagicMock())
    session_factory.return_value.__exit__ = MagicMock(return_value=False)
    redis = MagicMock()

    def fail_on_idle_sleep(_s):
        raise AssertionError("recycle did not fire: consumer kept polling")

    with (
        patch("workers.runner.streams.ensure_streams"),
        patch("workers.runner.streams.read_jobs", side_effect=fake_read),
        patch("workers.runner.streams.ack"),
        patch("workers.runner.time.sleep", side_effect=fail_on_idle_sleep),
    ):
        run_consumer(
            stream="doc.parse",
            consumer="c1",
            handler=handler,
            session_factory=session_factory,
            redis=redis,
            prefetch=1,
            poll_idle_ms=0,
            recycle_after=recycle_after,
        )
    return handled


def test_recycle_exits_after_n_successful_jobs():
    jobs = [{"n": i} for i in range(3)]
    handled = _run(jobs, recycle_after=3)
    assert [j["n"] for j in handled] == [0, 1, 2]  # exited, not blocked forever


def test_recycle_exits_mid_batch_no_early_exit():
    handled = _run([{"n": 0}, {"n": 1}], recycle_after=1)
    # the first job is handled, ACKed, THEN the loop exits — exactly one
    assert [j["n"] for j in handled] == [0]


def test_recycle_disabled_consumes_forever():
    """recycle_after=0 (the old default): never exits — the sleep sentinel
    proves the loop was still polling after the pool drained."""
    import pytest

    with pytest.raises(AssertionError, match="recycle did not fire"):
        _run([{"n": 0}], recycle_after=0)


def test_recycle_count_ignores_failed_jobs():
    """A failing job does not advance the recycle count — only successful
    handling does. 2 failures + 1 success with recycle_after=1 must exit
    only after the success (and the failures must still hit the error
    path, i.e. not be ACKed)."""
    failures = []

    def flaky(job):
        if job["n"] < 2:
            failures.append(job["n"])
            raise RuntimeError("boom")

    redis = MagicMock()
    pool = [{"n": 0}, {"n": 1}, {"n": 2}]

    def fake_read(r, stream, consumer, count=1, block_ms=5000):
        return [(f"{len(pool)}-0", pool.pop(0))] if pool else []

    session_factory = MagicMock()
    session_factory.return_value.__enter__ = MagicMock(return_value=MagicMock())
    session_factory.return_value.__exit__ = MagicMock(return_value=False)

    def fail_on_idle_sleep(_s):
        raise AssertionError("recycle did not fire: consumer kept polling")

    with (
        patch("workers.runner.streams.ensure_streams"),
        patch("workers.runner.streams.read_jobs", side_effect=fake_read),
        patch("workers.runner.streams.ack") as ack,
        patch("workers.runner.time.sleep", side_effect=fail_on_idle_sleep),
    ):
        run_consumer(
            stream="doc.parse",
            consumer="c1",
            handler=flaky,
            session_factory=session_factory,
            redis=redis,
            prefetch=1,
            poll_idle_ms=0,
            recycle_after=1,
        )
    # n=0 and n=1 failed (no ack), n=2 succeeded → ack, then exit
    assert failures == [0, 1]
    assert ack.call_count == 1
