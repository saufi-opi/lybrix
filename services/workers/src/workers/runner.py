"""Generic consumer loop: read → claim (Postgres lease) → handle → ack →
DLQ after max attempts (PRD §6.3, §6.6).

Every worker shares this runner so lease/ack/retry semantics live in
exactly one place. Handlers raise PlatformError; retryability comes
from the error taxonomy, not string matching.
"""

from __future__ import annotations

import logging
import time
from collections.abc import Callable
from typing import Any

from core.db.session import session_scope
from core.events import write_event
from core.queue import contracts, streams
from sqlalchemy.orm import Session

logger = logging.getLogger(__name__)


def run_consumer(
    *,
    stream: str,
    consumer: str,
    handler: Callable[[Session, Any], None],
    session_factory,
    redis,
    prefetch: int = 1,
    poll_idle_ms: int = 5_000,
    on_error: Callable[[Session, Any, Exception], None] | None = None,
) -> None:
    """Consume forever. ``handler(session, job_dict)`` runs inside one DB
    transaction; ack happens only after the transaction commits."""
    streams.ensure_streams(redis, (stream,))
    logger.info("consumer start stream=%s consumer=%s", stream, consumer)
    while True:
        try:
            jobs = streams.read_jobs(
                redis, stream, consumer, count=prefetch, block_ms=poll_idle_ms
            )
            for entry_id, job in jobs:
                try:
                    with session_scope(session_factory) as session:
                        handler(session, job)
                    streams.ack(redis, stream, entry_id)
                except Exception as exc:
                    # handler decided failure; record + keep the message
                    # unacked so XAUTOCLAIM/lease reaper can revisit it.
                    logger.exception("job failed stream=%s entry=%s", stream, entry_id)
                    if on_error is not None:
                        try:
                            with session_scope(session_factory) as session:
                                on_error(session, job, exc)
                        except Exception:
                            logger.exception("on_error also failed")
        except Exception:
            # never die on a transient Redis/PG blip; log and keep polling
            logger.exception("consumer loop error; retrying in 5s")
            time.sleep(5)


def record_job_error(
    session: Session,
    job: dict[str, Any],
    exc: Exception,
    stage: str,
    *,
    worker_id: str | None = None,
) -> None:
    """Default on_error: write an events row with the taxonomy code."""
    code = getattr(exc, "code", None)
    write_event(
        session,
        level="error",
        stage=stage,
        message=str(exc),
        doc_id=job.get("doc_id"),
        shard_idx=job.get("idx"),
        code=code.value if code is not None else None,
        worker_id=worker_id,
    )


__all__ = ["run_consumer", "record_job_error", "contracts", "streams"]
