"""Redis Streams producer/consumer-group primitives (PRD §4.2).

Each pipeline arrow is a Redis Stream with a consumer group. Job state
of record lives in Postgres, not Redis — if Redis is wiped, the janitor
re-enqueues everything not in a terminal state (§6.6 Requeue).
"""

from __future__ import annotations

import json
import logging
import uuid
from typing import Any

from redis import Redis

from core.config import Settings, get_settings

logger = logging.getLogger(__name__)

# Stream names, one per pipeline arrow.
STREAM_SPLIT = "doc.split"
STREAM_PARSE = "doc.parse"
STREAM_EMBED = "doc.embed"

ALL_STREAMS = (STREAM_SPLIT, STREAM_PARSE, STREAM_EMBED)
CONSUMER_GROUP = "rag-workers"


def make_redis(settings: Settings | None = None) -> Redis:
    s = settings or get_settings()
    # socket_timeout MUST exceed the blocking XREADGROUP block (5s): redis-py
    # 8.x sets the socket deadline to exactly the block duration when unset,
    # so the client read races the server's empty reply and raises a spurious
    # TimeoutError every idle poll (measured: raise at 5.03s; socket_timeout
    # 10 → 2/2 clean; protocol 2 vs 3 is irrelevant — empirically verified).
    return Redis.from_url(s.redis_url, decode_responses=True, socket_timeout=15)


def ensure_streams(r: Redis, streams: tuple[str, ...] = ALL_STREAMS) -> None:
    """Idempotently create streams + consumer group.

    MKSTREAM so a group can exist on an (as-yet) empty stream; the janitor
    relies on groups existing even before the first XADD.
    """
    for name in streams:
        try:
            r.xgroup_create(name, CONSUMER_GROUP, id="0", mkstream=True)
        except Exception as exc:  # BUSYGROUP: group already exists
            if "BUSYGROUP" not in str(exc):
                raise


def xadd_job(r: Redis, stream: str, payload: Any) -> str:
    """XADD a validated pydantic job payload as a single JSON field."""
    return r.xadd(stream, {"job": payload.model_dump_json()})


def read_jobs(
    r: Redis,
    stream: str,
    consumer: str,
    count: int = 1,
    block_ms: int = 5_000,
) -> list[tuple[str, dict[str, Any]]]:
    """Read up to ``count`` jobs for a consumer; returns [(entry_id, job)]."""
    groups = r.xreadgroup(
        CONSUMER_GROUP,
        consumer,
        {stream: ">"},
        count=count,
        block=block_ms,
    )
    out: list[tuple[str, dict[str, Any]]] = []
    for _stream, entries in groups:
        for entry_id, fields in entries:
            raw = fields.get("job", "{}")
            try:
                out.append((entry_id, json.loads(raw)))
            except json.JSONDecodeError:
                logger.error("unparseable job on %s: %s", stream, entry_id)
                # dead entry: ACK + XDEL so it never resurfaces via reclaim
                r.xack(stream, CONSUMER_GROUP, entry_id)
                try:
                    r.xdel(stream, entry_id)
                except Exception:
                    logger.warning("read_jobs: XDEL failed for %s/%s", stream, entry_id)
    return out


def ack(r: Redis, stream: str, entry_id: str) -> None:
    """ACK a processed job and XDEL it from the stream (trim-on-ACK).

    Streams are never trimmed automatically, so every processed job would
    otherwise live in the stream forever — 2026-09-12: doc.parse reached
    25,073 entries for 15,994 real shards (36% stale duplicates) because
    the janitor reclaim re-adds ACKed PEL entries, growing the stream in a
    loop. XDEL is best-effort: a failed delete must never fail a job that
    just completed.
    """
    r.xack(stream, CONSUMER_GROUP, entry_id)
    try:
        r.xdel(stream, entry_id)
    except Exception:
        logger.warning("ack: XDEL failed for %s/%s (best-effort)", stream, entry_id)


def claim_stale(
    r: Redis,
    stream: str,
    consumer: str,
    min_idle_ms: int,
    count: int = 10,
) -> list[tuple[str, dict[str, Any]]]:
    """XAUTOCLAIM entries idle beyond ``min_idle_ms`` — the Redis-side half
    of crash recovery; the Postgres lease (shards.lease_until) is the other."""
    cursor, entries, _ = r.xautoclaim(
        stream, CONSUMER_GROUP, consumer, min_idle_ms=min_idle_ms, count=count
    )
    out = []
    for entry_id, fields in entries:
        try:
            out.append((entry_id, json.loads(fields.get("job", "{}"))))
        except json.JSONDecodeError:
            r.xack(stream, CONSUMER_GROUP, entry_id)
    return out


def undelivered_count(r: Redis, stream: str) -> int:
    """Entries the group has never seen (Redis ``lag``) with a NULL fallback.

    ``XINFO GROUPS.lag`` becomes ``None`` once the group's bookkeeping is
    untrustworthy — most commonly after XDEL/XTRIM removed entries that
    the counters already accounted (2026-09-12: cleaning duplicate embed
    jobs left doc.embed lag=NULL and the dashboard showed waiting=1 for a
    73-job backlog). Fast path returns the int directly; on None we count
    the undelivered tail with an exclusive XRANGE scan after the group's
    last-delivered-id (pages of 500, same cursor semantics as the janitor
    dedup scan). No group at all → 0. Redis errors propagate — callers
    decide policy; silently returning 0 hides anomalies.
    """
    try:
        for grp in r.xinfo_groups(stream):
            if grp.get("name") != CONSUMER_GROUP:
                continue
            lag = grp.get("lag")
            if lag is not None:
                return int(lag)
            cursor = grp.get("last-delivered-id") or "0-0"
            start = f"({cursor}"  # exclusive: entries AT the cursor are delivered
            total = 0
            while True:
                page = r.xrange(stream, min=start, max="+", count=500)
                total += len(page)
                if len(page) < 500:
                    return total
                start = f"({page[-1][0]}"
        return 0  # no group → nothing is undelivered for this group
    except Exception:
        logger.exception("undelivered_count failed on %s", stream)
        raise


def queue_depth(r: Redis, stream: str) -> int:
    """Undelivered + pending entries — the number backpressure cares about.

    XLEN counts EVERY entry ever written to the stream (nothing trims it),
    so on a long-lived stream it only grows and eventually trips the
    backpressure cap even when the queue is actually empty. The real
    backlog is undelivered entries (group lag, with a live-count fallback
    when Redis reports None — see undelivered_count) plus ``pending``
    (delivered but unacked / PEL).
    """
    try:
        pending = int(r.xpending(stream, CONSUMER_GROUP)["pending"] or 0)
    except Exception:
        pending = 0
    return undelivered_count(r, stream) + pending


def quarantine(
    session,
    r: Redis,
    stream: str,
    entry_id: str,
    raw_job: str,
    times_delivered: int,
    last_error: str | None = None,
) -> None:
    """Quarantine a poison job: one DLQ events row, then drop the entry.

    The terminal half of the janitor reclaim path (R-8). A job whose
    handler always raises (e.g. a doc deleted mid-parse → KeyError in the
    parser) is otherwise redelivered forever: deliver → raise → unacked
    in the PEL → XAUTOCLAIM → re-add → deliver … The reclaim re-add also
    resets the entry, so nothing bounds the loop. Once the caller decides
    an entry is over the delivery cap, this writes one events row
    (level=error, stage=dlq, code=DLQ) carrying the raw job JSON, then
    ACKs and deletes the entry — both Redis calls best-effort, never
    raising: dropping the stream entry is the point, the events row is
    the durable record. ``session`` is the caller's PG session (the
    janitor pass transaction) — no new session is opened here.
    doc_id/shard_idx are parsed from the raw job when possible;
    unparseable jobs are still quarantined.
    """
    from core.events import write_event

    doc_id = None
    shard_idx = None
    try:
        payload = json.loads(raw_job)
        if isinstance(payload, dict):
            if payload.get("doc_id"):
                doc_id = uuid.UUID(str(payload["doc_id"]))
            if payload.get("idx") is not None:
                shard_idx = int(payload["idx"])
    except (ValueError, TypeError):
        pass  # malformed JSON / bad uuid / bad idx: quarantine without links

    detail: dict[str, Any] = {"detail": raw_job}
    if last_error:
        detail["last_error"] = last_error
    write_event(
        session,
        "error",
        "dlq",
        f"job quarantined from {stream} after {times_delivered} deliveries",
        doc_id=doc_id,
        shard_idx=shard_idx,
        code="DLQ",
        context=detail,
    )
    try:
        r.xack(stream, CONSUMER_GROUP, entry_id)
    except Exception:
        logger.warning("quarantine: XACK failed for %s/%s", stream, entry_id)
    try:
        r.xdel(stream, entry_id)
    except Exception:
        logger.warning("quarantine: XDEL failed for %s/%s", stream, entry_id)


def new_consumer_name(prefix: str) -> str:
    return f"{prefix}-{uuid.uuid4().hex[:8]}"
