"""Janitor — continuous reaper/requeue/escalate/stuck/TTL (PRD §6.6).

Runs every JANITOR_INTERVAL_S. This is the component that makes
`docker kill` on a parser a non-event: expired leases go back to
pending within one pass.
"""

from __future__ import annotations

import json
import logging
import time
from datetime import UTC, datetime, timedelta

from core.config import Settings, get_settings
from core.db import repo
from core.db.models import Chunk, DocState, Document, Shard
from core.db.session import session_scope
from core.queue import streams
from sqlalchemy import select

logger = logging.getLogger(__name__)

_TERMINAL_STATES = {DocState.READY, DocState.FAILED, DocState.ARCHIVED, DocState.PARTIAL}

SCAN_PAGE = 500

_last_bucket: datetime | None = None


def write_metrics_rollup(session, redis, settings: Settings) -> bool:
    """One row per minute bucket (PRD §10.1): per-minute deltas from the
    previous bucket's snapshot + windowed p50/p95 from shards.done_at.
    search_p95_ms/search_count stay None for now — no search-latency
    capture exists yet (UsageMiddleware records counts, not durations)."""
    global _last_bucket
    now = datetime.now(UTC)
    bucket = now.replace(second=0, microsecond=0)
    window_start = bucket - timedelta(minutes=1)
    rows = (
        session.execute(
            select(Shard).where(
                Shard.done_at.is_not(None),
                Shard.done_at >= window_start,
                Shard.done_at < bucket,
            )
        )
        .scalars()
        .all()
    )
    if not rows and _last_bucket == bucket:
        return False
    # Idempotent upsert on the bucket PK; _last_bucket updated on success —
    # the janitor is a single process, a restart just re-writes one bucket.
    durations = sorted(r.duration_ms or 0 for r in rows)

    def pct(sorted_list: list[int], p: int) -> int | None:
        if not sorted_list:
            return None
        i = max(0, round((len(sorted_list) - 1) * p / 100))
        return sorted_list[i]

    # try/except → {}: a Redis queue_depth failure must never abort the
    # janitor pass transaction (the rollup is informational).
    queue_depth: dict[str, int] = {}
    for name in streams.ALL_STREAMS:
        try:
            queue_depth[name] = streams.queue_depth(redis, name)
        except Exception:
            logger.warning("rollup: queue_depth read failed for %s", name)
    chunks_embedded = 0
    try:
        from sqlalchemy import func

        chunks_embedded = int(
            session.execute(
                select(func.count())
                .select_from(Chunk)
                .where(Chunk.embedded_at.is_not(None))
                .where(Chunk.embedded_at >= window_start, Chunk.embedded_at < bucket)
            ).scalar_one()
        )
    except Exception:
        logger.warning("rollup: chunks_embedded count failed")

    from core.db.models import MetricsRollup

    session.merge(
        MetricsRollup(
            bucket=bucket,
            pages_parsed=sum(r.page_end - r.page_start + 1 for r in rows),
            shards_done=len(rows),
            shards_failed=sum(1 for r in rows if r.state == "failed"),
            chunks_embedded=chunks_embedded,
            parse_p50_ms=pct(durations, 50),
            parse_p95_ms=pct(durations, 95),
            peak_rss_p95_mb=pct(sorted(r.peak_rss_mb or 0 for r in rows), 95),
            queue_depth=queue_depth,
        )
    )
    _last_bucket = bucket
    return True


def pending_job_doc_ids(r, stream: str) -> set[str] | None:
    """doc_ids holding an UNDELIVERED job on ``stream`` (any stream).

    Precise semantics (stream is never trimmed, so a full XRANGE would
    see every job ever ACKed and wrongly suppress future re-enqueues):
      1. everything after the group's last-delivered-id (undelivered), plus
      2. the PEL (delivered-but-unacked) — claimed by a live worker or
         waiting for the reclaim step, which runs BEFORE the requeue
         sweeps in every pass.
    Returns ``None`` if the scan itself fails — the caller MUST skip the
    sweep rather than blind-add (blind re-adding is exactly how the
    2026-09-11 doc.embed flood happened: ~3.2k duplicate jobs).
    """
    try:
        groups = [g for g in r.xinfo_groups(stream) if g.get("name") == streams.CONSUMER_GROUP]
        if not groups:
            return set()
        last_delivered = groups[0].get("last-delivered-id") or "0-0"

        ids: set[str] = set()
        start = f"({last_delivered}"  # exclusive: entries AT it are delivered
        while True:
            page = r.xrange(stream, min=start, max="+", count=SCAN_PAGE)
            if not page:
                break
            for _entry_id, fields in page:
                raw = (fields or {}).get("job")
                if not raw:
                    continue
                try:
                    doc_id = json.loads(raw).get("doc_id")
                except (json.JSONDecodeError, AttributeError):
                    continue
                if doc_id:
                    ids.add(doc_id)
            if len(page) < SCAN_PAGE:
                break
            start = f"({page[-1][0]}"

        # PEL: delivered but unacked (live claim or awaiting reclaim)
        pel = r.xpending_range(stream, streams.CONSUMER_GROUP, "-", "+", SCAN_PAGE)
        for p in pel or []:
            entry_id = p.get("message_id")
            if not entry_id:
                continue
            for _eid, fields in r.xrange(stream, min=entry_id, max=entry_id):
                raw = (fields or {}).get("job")
                if not raw:
                    continue
                try:
                    doc_id = json.loads(raw).get("doc_id")
                except (json.JSONDecodeError, AttributeError):
                    continue
                if doc_id:
                    ids.add(doc_id)
    except Exception:
        logger.exception("pending_job_doc_ids scan failed on %s", stream)
        return None
    return ids


def pending_embed_docs(r, stream: str = streams.STREAM_EMBED) -> set[str] | None:
    """doc_ids holding an UNDELIVERED embed job on ``stream``.

    Thin wrapper over pending_job_doc_ids kept for the embed sweep's
    call sites and tests. An entry delivered to a live embedder <20 min
    ago is invisible to the reclaim and IS covered here via the PEL —
    re-adding it would duplicate in-flight work, which is worse than a
    skipped sweep pass.
    """
    return pending_job_doc_ids(r, stream)


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

    # 3. Requeue: non-terminal docs with no queue entry (Redis loss, §6.6).
    #    Dedup against undelivered split jobs first (R-6, same class as the
    #    2026-09-11 embed flood): an UPLOADED doc whose split job sits
    #    unacked must not be re-XADDed every pass. Scan failure → skip the
    #    requeue entirely, never blind-add.
    reenqueued = 0
    docs = (
        session.execute(select(Document).where(~Document.state.in_(_TERMINAL_STATES)))
        .scalars()
        .all()
    )
    from core.queue import contracts

    queued_split_ids = pending_job_doc_ids(redis, streams.STREAM_SPLIT)
    if queued_split_ids is None:
        logger.warning("doc.split requeue skipped: stream scan failed (no blind re-add)")
        queued_split_ids = set()
        split_scan_failed = True
    else:
        split_scan_failed = False
    for doc in docs:
        if doc.state == DocState.UPLOADED and not split_scan_failed:
            if str(doc.id) in queued_split_ids:
                continue  # a split job is already waiting — never double-add
            streams.xadd_job(
                redis, streams.STREAM_SPLIT, contracts.SplitJob(doc_id=doc.id, source_uri=doc.source_uri)
            )
            reenqueued += 1

    # 4. Reclaim: PEL entries stranded by dead consumers (worker recreated /
    #    crashed between XREADGROUP and XACK). XAUTOCLAIM them, re-add as
    #    fresh stream entries, ack the old one. PG-lease idempotency makes
    #    duplicates safe; min_idle must exceed the longest legit job
    #    (SHARD_LEASE_SECONDS=900) so live parses are never double-claimed.
    #    MUST run before the settled-book sweep: re-added entries become
    #    undelivered, so the sweep's dedup scan sees them.
    #
    #    Delivery cap (R-8): the runner leaves failed entries unacked in
    #    the PEL, so this reclaim revisits them forever — and re-adding
    #    resets the entry, so a job whose handler always raises (vanished
    #    doc → KeyError in the parser) loops deliver→raise→reclaim
    #    unbounded. Entries at/over DELIVERY_CAP are quarantined (DLQ
    #    events row + XACK/XDEL) instead of re-added. The cap read is
    #    fail-open: if the XPENDING lookup fails or returns nothing, the
    #    entry re-adds as before — quarantine only ever fires on a real
    #    delivery count.
    reclaimed = 0
    quarantined = 0
    delivery_cap = max(5, settings.max_shard_attempts + 1)
    for stream in streams.ALL_STREAMS:
        try:
            res = redis.xautoclaim(
                stream, streams.CONSUMER_GROUP, "janitor-reclaim",
                min_idle_time=20 * 60 * 1000, count=50,
            )
        except Exception:
            continue
        entries = res[1] if isinstance(res, (list, tuple)) and len(res) > 1 else []
        for entry_id, fields in entries:
            job = (fields or {}).get("job")
            if not job:
                redis.xack(stream, streams.CONSUMER_GROUP, entry_id)
                redis.xdel(stream, entry_id)  # trim the husk too
                continue
            # delivery count for THIS entry (fail-open: query trouble or an
            # empty answer → None → re-add, never quarantine on a guess)
            times_delivered = None
            try:
                pending = redis.xpending_range(
                    stream, streams.CONSUMER_GROUP, min=entry_id, max=entry_id, count=1
                )
                if pending:
                    times_delivered = int(pending[0].get("times_delivered") or 0)
            except Exception:
                logger.warning(
                    "reclaim: XPENDING failed on %s/%s — capping disabled for this entry",
                    stream,
                    entry_id,
                )
            if times_delivered is not None and times_delivered >= delivery_cap:
                streams.quarantine(session, redis, stream, entry_id, job, times_delivered)
                quarantined += 1
                continue
            redis.xadd(stream, {"job": job})
            redis.xack(stream, streams.CONSUMER_GROUP, entry_id)
            redis.xdel(stream, entry_id)  # old entry trimmed; fresh copy re-added
            reclaimed += 1

    # 5. Settled-but-never-enqueued sweep: a parser that dies between the
    #     PG commit of the last shard and the Redis XADD loses the embed job
    #     forever (real incident 2026-09-10: 7 books). Settled = every shard
    #     done/failed (the same condition the parser checks). Re-adding the
    #     embed job is idempotent (chunk ON CONFLICT DO NOTHING + chunk_hash
    #     point ids), so duplicates are harmless — BUT the sweep runs every
    #     pass, so it must dedup against jobs already waiting on the stream
    #     (real incident 2026-09-11: no dedup + an unacked job ⇒ 3.2k dupes).
    from core.queue import contracts

    embed_swept = 0
    queued_ids = pending_embed_docs(redis, streams.STREAM_EMBED)
    if queued_ids is not None:
        for doc in docs:
            if doc.state != DocState.PARSING:
                continue
            if not repo.book_settled(doc):
                continue
            if str(doc.id) in queued_ids:
                continue  # an embed job is already waiting — never double-add
            streams.xadd_job(
                redis, streams.STREAM_EMBED, contracts.EmbedJob(doc_id=doc.id)
            )
            write_evt(
                session,
                level="warn",
                stage="janitor",
                code="EMBED_RESCUE",
                message="settled book found without embed job — re-enqueued",
                doc_id=doc.id,
            )
            embed_swept += 1
    else:
        logger.warning("embed sweep skipped: stream scan failed (no blind re-add)")

    # 6. Stuck-document warning (§6.6) — informational only
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

    # 7. Metrics rollup (PRD §10.1): one row per minute bucket from real
    # data. Runs inside the pass transaction; a Redis read failure is
    # caught inside write_metrics_rollup and never aborts the pass.
    rolled_up = 0
    current_bucket = now.replace(second=0, microsecond=0)
    if _last_bucket is None or current_bucket > _last_bucket:
        try:
            if write_metrics_rollup(session, redis, settings):
                rolled_up = 1
        except Exception:
            logger.exception("metrics rollup write failed")

    return {"requeued_leases": requeued, "escalated": escalated, "reenqueued": reenqueued, "embed_swept": embed_swept, "reclaimed": reclaimed, "quarantined": quarantined, "stuck_warned": warned, "rollup": rolled_up}


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
