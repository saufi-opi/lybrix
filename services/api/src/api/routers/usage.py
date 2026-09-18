"""Usage summary (web console Phase 2): per-key request counts from key_usage.

One GROUP BY over key_usage joined to api_keys, bounded by a period cutoff.
At our scale (~few hundred rows/day) a live aggregate is fine (PRD §11).
"""

from __future__ import annotations

from datetime import UTC, datetime, timedelta
from typing import Literal

from core.db.models import ApiKey, KeyUsage
from fastapi import APIRouter, Depends
from sqlalchemy import func, select
from sqlalchemy.orm import Session

from api.deps import get_session, require_scope

router = APIRouter(prefix="/v1/usage", tags=["usage"])

Period = Literal["today", "24h", "7d", "30d"]
_PERIOD_HOURS = {"24h": 24, "7d": 24 * 7, "30d": 24 * 30}


def _cutoff(period: Period, now: datetime) -> datetime:
    if period == "today":
        return now.replace(hour=0, minute=0, second=0, microsecond=0)
    return now - timedelta(hours=_PERIOD_HOURS[period])


@router.get("/summary")
def usage_summary(
    period: Period = "24h",
    key=Depends(require_scope("admin")),
    session: Session = Depends(get_session),
):
    """{period, total, by_key: [{key_id, name, calls, last_used_at}]}, calls desc."""
    start = _cutoff(period, datetime.now(UTC))
    rows = session.execute(
        select(
            ApiKey.id,
            ApiKey.name,
            func.count(KeyUsage.id).label("calls"),
            func.max(KeyUsage.created_at).label("last"),
        )
        .join(KeyUsage, KeyUsage.api_key_id == ApiKey.id)
        .where(KeyUsage.created_at >= start)
        .group_by(ApiKey.id, ApiKey.name)
        .order_by(func.count(KeyUsage.id).desc())
    ).all()
    return {
        "period": period,
        "total": int(sum(r[2] for r in rows)),
        "by_key": [
            {
                "key_id": str(r[0]),
                "name": r[1],
                "calls": int(r[2]),
                "last_used_at": r[3],
            }
            for r in rows
        ],
    }
