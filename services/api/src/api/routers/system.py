"""System endpoints: health, queues, workers, metrics (PRD §9)."""

from __future__ import annotations

from typing import Any

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
        return (
            "ok" if httpx.get(f"{url.rstrip('/')}/health", timeout=2).status_code == 200 else "down"
        )
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
        "qdrant": _check_qdrant(
            s.qdrant_url
        ),  # /collections probe WITH api key (/health 404s + needs auth)
        "tei_query": _check_url(s.tei_query_url),
    }


@router.get("/queues")
def queues():
    """Depth + consumer lag per stream (PRD §10.1).

    undelivered comes from streams.undelivered_count() — NULL-safe lag with
    a live-count fallback (2026-09-12: XINFO lag=None after XDEL cleanup
    collapsed to 0 here, and the main dashboard disagreed with the pipeline
    page). Errors propagate: a dead Redis must not read as an empty queue.
    """
    r: Redis = streams.make_redis()
    out = {}
    for name in streams.ALL_STREAMS:
        try:
            pending = r.xpending(name, streams.CONSUMER_GROUP)
            out[name] = {
                # "length" is XLEN (every entry ever written — streams are
                # never trimmed), NOT the live backlog. Kept for history.
                "length": r.xlen(name),
                "pending": pending["pending"] if pending else 0,
                "undelivered": streams.undelivered_count(r, name),
            }
        except Exception:
            raise
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
            # waiting = undelivered + PEL, NOT XLEN — streams are never
            # trimmed so XLEN only ever grows (it showed 20,703 "waiting"
            # on a queue with 0 undelivered + 23 pending).
            # undelivered_count() handles Redis lag=None (untrusted
            # bookkeeping, e.g. after XDEL) with a live count fallback —
            # int(grp.get("lag") or 0) collapsed that to 0 and hid a
            # 73-job backlog on 2026-09-12.
            grp = next(
                (g for g in r.xinfo_groups(name) if g.get("name") == streams.CONSUMER_GROUP),
                None,
            )
            undelivered = (
                streams.undelivered_count(r, name)
                if grp is not None
                else 0
            )
            pend = r.xpending_range(name, streams.CONSUMER_GROUP, min="-", max="+", count=100)
            lane["waiting"] = undelivered + len(pend)
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
    counts: dict[str, Any] = {}
    try:
        row = (
            session.execute(
                text(
                    "SELECT"
                    "  count(*) FILTER (WHERE state='ready') AS ready,"
                    "  count(*) FILTER (WHERE state='parsing') AS parsing,"
                    "  count(*) FILTER (WHERE state='failed') AS failed,"
                    "  count(*) AS total FROM documents"
                )
            )
            .mappings()
            .one()
        )
        counts["docs_ready"] = row["ready"]
        counts["docs_parsing"] = row["parsing"]
        counts["docs_failed"] = row["failed"]
        counts["docs_total"] = row["total"]

        # parsing breakdown (derived, 2026-09-12): state='parsing' hides two
        # phases — shards still converting vs whole-book settled waiting for
        # the embedder (whole-book barrier). Break them out so the dashboard
        # can show parser vs embedder lag separately. No new enum value —
        # derived from shards counters (PG is the source of truth).
        brow = (
            session.execute(
                text(
                    "SELECT"
                    "  count(*) FILTER (WHERE total_shards IS NOT NULL AND shards_done >= total_shards) AS awaiting_embed,"
                    "  count(*) FILTER (WHERE total_shards IS NULL OR shards_done < total_shards) AS parsing_active"
                    " FROM documents WHERE state='parsing'"
                )
            )
            .mappings()
            .one()
        )
        counts["docs_awaiting_embed"] = brow["awaiting_embed"]
        counts["docs_parsing_active"] = brow["parsing_active"]

        srow = (
            session.execute(
                text(
                    "SELECT"
                    "  count(*) FILTER (WHERE state='done') AS done,"
                    "  count(*) FILTER (WHERE state='pending') AS pending,"
                    "  count(*) FILTER (WHERE state='failed') AS failed,"
                    "  count(*) FILTER (WHERE state='running') AS running,"
                    "  count(*) AS total FROM shards"
                )
            )
            .mappings()
            .one()
        )
        counts["shards_done"] = srow["done"]
        counts["shards_pending"] = srow["pending"]
        counts["shards_failed"] = srow["failed"]
        counts["shards_running"] = srow["running"]
        counts["shards_total"] = srow["total"]

        # Full state breakdown — dashboards must NOT derive per-state counts
        # from ?limit=N document lists (the /dashboard "ready" card read the
        # first 200 rows and undercounted vs the pipeline aggregate forever).
        strow = (
            session.execute(text("SELECT state, count(*) AS n FROM documents GROUP BY state"))
            .mappings()
            .all()
        )
        counts["docs_states"] = {r["state"]: r["n"] for r in strow}
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
