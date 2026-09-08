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
    return Redis.from_url(s.redis_url, decode_responses=True)


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
                r.xack(stream, CONSUMER_GROUP, entry_id)
    return out


def ack(r: Redis, stream: str, entry_id: str) -> None:
    r.xack(stream, CONSUMER_GROUP, entry_id)


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


def queue_depth(r: Redis, stream: str) -> int:
    """Pending + undelivered entries — the number backpressure cares about."""
    try:
        pending = r.xpending(stream, CONSUMER_GROUP)["pending"]
    except Exception:
        pending = 0
    return int(r.xlen(stream)) + int(pending or 0)


def new_consumer_name(prefix: str) -> str:
    return f"{prefix}-{uuid.uuid4().hex[:8]}"
