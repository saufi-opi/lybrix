"""System endpoints: health, queues, workers, metrics (PRD §9)."""

from __future__ import annotations

import httpx
from core.config import get_settings
from core.observability.metrics import metrics
from core.queue import streams
from fastapi import APIRouter, Depends
from redis import Redis
from sqlalchemy import text
from sqlalchemy.orm import Session

from api.deps import get_session

router = APIRouter(prefix="/v1/system", tags=["system"])


def _check_postgres(session: Session) -> str:
    try:
        session.execute(text("SELECT 1"))
        return "ok"
    except Exception:
        return "down"


def _check_redis() -> str:
    try:
        streams.make_redis().ping()
        return "ok"
    except Exception:
        return "down"


def _check_url(url: str) -> str:
    try:
        return "ok" if httpx.get(f"{url.rstrip('/')}/health", timeout=2).status_code == 200 else "down"
    except Exception:
        return "down"


def _bearer(key: str | None) -> dict[str, str]:
    return {"Authorization": f"Bearer {key}"} if key else {}


def _check_qdrant(url: str) -> str:
    """Qdrant has no /health — probe /collections root with the API key."""
    try:
        s = get_settings()
        r = httpx.get(
            url.rstrip("/") + "/collections",
            timeout=2,
            headers={"Authorization": f"Bearer {s.qdrant_api_key}"} if s.qdrant_api_key else {},
        )
        return "ok" if r.status_code == 200 else "down"
    except Exception:
        return "down"


@router.get("/health")
def health(session: Session = Depends(get_session)):
    s = get_settings()
    return {
        "status": "ok" if _check_postgres(session) == "ok" else "degraded",
        "postgres": _check_postgres(session),
        "redis": _check_redis(),
        "qdrant": _check_url(s.qdrant_url),
        "tei_query": _check_url(s.tei_query_url),
    }


@router.get("/queues")
def queues():
    """Depth + consumer lag per stream (PRD §10.1)."""
    r: Redis = streams.make_redis()
    out = {}
    for name in streams.ALL_STREAMS:
        try:
            pending = r.xpending(name, streams.CONSUMER_GROUP)
            out[name] = {
                "length": r.xlen(name),
                "pending": pending["pending"] if pending else 0,
            }
        except Exception:
            out[name] = {"length": None, "pending": None}
    return out


@router.get("/pipeline")
def pipeline(session: Session = Depends(get_session)):
    """Aggregate snapshot for the pipeline live view (web /pipeline page).

    One call answers: is every component up, how deep is each queue lane,
    who is holding in-flight work, and how fast are shards settling.
    """
    s = get_settings()
    r: Redis = streams.make_redis()

    # -- component health (reuse /health checks + ollama ingest probe) ------
    components = {
        "postgres": _check_postgres(session),
        "redis": _check_redis(),
        "qdrant": _check_qdrant(s.qdrant_url),
        "tei_query": _check_url(s.tei_query_url),
    }
    # embed backend: ollama (Jetson) answers /api/tags, TEI answers /health
    try:
        components["embed_backend"] = (
            "ok"
            if httpx.get(f"{s.tei_ingest_url.rstrip('/')}/api/tags", timeout=2).status_code == 200
            else "down"
        )
    except Exception:
        components["embed_backend"] = "down"

    # -- queue lanes: waiting / in-flight / stale / consumers ---------------
    lanes = {}
    for name in streams.ALL_STREAMS:
        lane = {"waiting": None, "in_flight": None, "stale": 0, "consumers": 0}
        try:
            lane["waiting"] = r.xlen(name)
            pend = r.xpending_range(name, streams.CONSUMER_GROUP, min="-", max="+", count=100)
            lane["in_flight"] = len(pend)
            now_ms = r.time()[0]
            for entry in pend:
                if now_ms - int(entry["time_since_delivered"]) > 900_000:  # 15 min
                    lane["stale"] += 1
            consumers = r.xinfo_consumers(name, streams.CONSUMER_GROUP)
            lane["consumers"] = sum(1 for c in consumers if c.get("idle", 10**9) < 300_000)
        except Exception:
            pass
        lanes[name] = lane

    # -- document/shard counters --------------------------------------------
    counts: dict[str, int] = {}
    try:
        row = session.execute(
            text(
                "SELECT"
                "  count(*) FILTER (WHERE state='ready') AS ready,"
                "  count(*) FILTER (WHERE state='parsing') AS parsing,"
                "  count(*) FILTER (WHERE state='failed') AS failed,"
                "  count(*) AS total FROM documents"
            )
        ).mappings().one()
        counts["docs_ready"] = row["ready"]
        counts["docs_parsing"] = row["parsing"]
        counts["docs_failed"] = row["failed"]
        counts["docs_total"] = row["total"]

        srow = session.execute(
            text(
                "SELECT"
                "  count(*) FILTER (WHERE state='done') AS done,"
                "  count(*) FILTER (WHERE state='pending') AS pending,"
                "  count(*) FILTER (WHERE state='failed') AS failed,"
                "  count(*) FILTER (WHERE state='running') AS running,"
                "  count(*) AS total FROM shards"
            )
        ).mappings().one()
        counts["shards_done"] = srow["done"]
        counts["shards_pending"] = srow["pending"]
        counts["shards_failed"] = srow["failed"]
        counts["shards_running"] = srow["running"]
        counts["shards_total"] = srow["total"]
    except Exception:
        pass

    # -- in-flight parse detail: shards with a live lease --------------------
    in_flight = []
    try:
        rows = session.execute(
            text(
                "SELECT d.title, s.idx, s.page_start, s.page_end,"
                " s.worker_id, s.lease_until"
                " FROM shards s JOIN documents d ON d.id = s.doc_id"
                " WHERE s.state='running' LIMIT 12"
            )
        )
        in_flight = [dict(x) for x in rows.mappings()]
    except Exception:
        pass

    # -- qdrant points (chunks collection) -----------------------------------
    qdrant_points = None
    try:
        qr = httpx.get(
            s.qdrant_url.rstrip("/") + "/collections/chunks",
            timeout=3,
            headers={"Authorization": f"Bearer {s.qdrant_api_key}"} if s.qdrant_api_key else {},
        )
        if qr.status_code == 200:
            qdrant_points = qr.json().get("result", {}).get("points_count")
    except Exception:
        pass

    return {
        "components": components,
        "lanes": lanes,
        "counts": counts,
        "in_flight_parse": in_flight,
        "qdrant_points": qdrant_points,
        "server_time": None,  # web layer stamps local time
    }


@router.get("/metrics")
def get_metrics():
    """JSON counters, Prometheus-convertible shape (§10.4)."""
    return metrics().snapshot()
