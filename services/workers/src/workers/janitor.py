"""Janitor — continuous reaper/requeue/escalate/stuck/TTL (PRD §6.6).

Runs every JANITOR_INTERVAL_S. This is the component that makes
`docker kill` on a parser a non-event: expired leases go back to
pending within one pass.
"""

from __future__ import annotations

import logging
import time
from datetime import UTC, datetime, timedelta

from core.config import Settings, get_settings
from core.db import repo
from core.db.models import DocState, Document, Shard
from core.db.session import session_scope
from core.queue import streams
from sqlalchemy import select

logger = logging.getLogger(__name__)

_TERMINAL_STATES = {DocState.READY, DocState.FAILED, DocState.ARCHIVED, DocState.PARTIAL}


def janitor_pass(session, redis, settings: Settings) -> dict:
    """One sweep; returns counters for logging/metrics."""
    now = datetime.now(UTC)

    # 1. Reaper: expired running leases → pending (§6.6)
    requeued = repo.requeue_expired_leases(session)

    # 2. Escalate: shards with attempts >= max → failed + event (§6.6)
    escalated = 0
    stuck_shards = (
        session.execute(
            select(Shard).where(
                Shard.state == ShardState_helper_pending(),
                Shard.attempts >= settings.max_shard_attempts,
            )
        )
        .scalars()
        .all()
    )
    for shard in stuck_shards:
        shard.state = "failed"
        shard.error_code = "SHARD_TIMEOUT"
        shard.error_detail = f"escalated after {shard.attempts} attempts"
        write_evt(
            session,
            level="error",
            stage="parse",
            code="SHARD_TIMEOUT",
            message=f"shard escalated to failed after {shard.attempts} attempts",
            doc_id=shard.doc_id,
            shard_idx=shard.idx,
        )
        escalated += 1
        doc = repo.get_document(session, shard.doc_id)
        if doc is not None:
            doc.shards_failed = (doc.shards_failed or 0) + 1

    # 3. Requeue: non-terminal docs with no queue entry (Redis loss, §6.6)
    reenqueued = 0
    docs = (
        session.execute(select(Document).where(~Document.state.in_(_TERMINAL_STATES)))
        .scalars()
        .all()
    )
    for doc in docs:
        if doc.state == DocState.UPLOADED:
            from core.queue import contracts

            streams.xadd_job(
                redis, streams.STREAM_SPLIT, contracts.SplitJob(doc_id=doc.id, source_uri=doc.source_uri)
            )
            reenqueued += 1

    # 4. Stuck detector: same non-terminal state > STUCK_MINUTES → warn (§6.6)
    warned = 0
    cutoff = now - timedelta(minutes=settings.stuck_minutes)
    for doc in docs:
        if doc.state in _TERMINAL_STATES:
            continue
        if doc.updated_at and doc.updated_at < cutoff:
            write_evt(
                session,
                level="warn",
                stage="janitor",
                code="STUCK_DOCUMENT",
                message=f"document non-terminal for over {settings.stuck_minutes} min (state={doc.state.value})",
                doc_id=doc.id,
            )
            warned += 1

    return {"requeued_leases": requeued, "escalated": escalated, "reenqueued": reenqueued, "stuck_warned": warned}


def write_evt(session, **kwargs) -> None:
    from core.events import write_event

    write_event(session, kwargs.pop("level"), kwargs.pop("stage"), kwargs.pop("message"), **kwargs)


def ShardState_helper_pending():
    from core.db.models import ShardState

    return ShardState.PENDING


def main() -> None:  # pragma: no cover - process entry
    from core.db.session import make_engine, make_session_factory
    from core.observability.logging import configure_logging

    settings = get_settings()
    configure_logging(settings.log_level)
    logger.info("janitor starting (interval=%ss)", settings.janitor_interval_s)

    factory = make_session_factory(make_engine(settings))
    redis = streams.make_redis(settings)

    while True:
        try:
            with session_scope(factory) as session:
                stats = janitor_pass(session, redis, settings)
                if any(stats.values()):
                    logger.info("janitor pass: %s", stats)
        except Exception:
            logger.exception("janitor pass failed")
        time.sleep(settings.janitor_interval_s)


if __name__ == "__main__":
    main()
