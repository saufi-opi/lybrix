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


@router.get("/metrics")
def get_metrics():
    """JSON counters, Prometheus-convertible shape (§10.4)."""
    return metrics().snapshot()
