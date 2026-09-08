"""Event log + SSE stream (PRD §8.1 Logs, §9 /v1/events/stream).

A progress bar that requires a refresh is not a progress bar — the UI
subscribes here for live updates.
"""

from __future__ import annotations

import asyncio
import json

from fastapi import APIRouter, Depends, Query
from fastapi.responses import StreamingResponse
from sqlalchemy import select
from sqlalchemy.orm import Session

from api.deps import get_session
from core.db.models import Event

router = APIRouter(prefix="/v1/events", tags=["events"])


@router.get("")
def list_events(
    doc_id: str | None = None,
    level: str | None = None,
    stage: str | None = None,
    limit: int = Query(default=100, le=500),
    session: Session = Depends(get_session),
):
    stmt = select(Event).order_by(Event.created_at.desc()).limit(limit)
    if doc_id:
        stmt = stmt.where(Event.doc_id == doc_id)
    if level:
        stmt = stmt.where(Event.level == level)
    if stage:
        stmt = stmt.where(Event.stage == stage)
    return list(session.execute(stmt).scalars())


@router.get("/stream")
async def event_stream(poll_s: float = 2.0):
    """SSE feed of recent events, polled from Postgres.

    v1 keeps this deliberately dumb: one query per poll tick, no broker
    fan-out. At 5–50 concurrent UI clients this is a rounding error on
    Postgres, and it removes a pub/sub dependency from the UI path.
    """

    async def gen():
        last_id = 0
        while True:
            from api.deps import get_session as _gs

            gen_session = next(_gs())
            try:
                stmt = (
                    select(Event)
                    .where(Event.id > last_id)
                    .order_by(Event.id.asc())
                    .limit(100)
                )
                rows = list(gen_session.execute(stmt).scalars())
                for row in rows:
                    last_id = max(last_id, row.id)
                    payload = {
                        "id": row.id,
                        "doc_id": str(row.doc_id) if row.doc_id else None,
                        "shard_idx": row.shard_idx,
                        "level": row.level,
                        "stage": row.stage,
                        "code": row.code,
                        "message": row.message,
                        "created_at": row.created_at.isoformat(),
                    }
                    yield f"data: {json.dumps(payload)}\n\n"
            finally:
                gen_session.close()
            await asyncio.sleep(poll_s)

    return StreamingResponse(gen(), media_type="text/event-stream")
