"""write_event() helper — the events table is the product surface (PRD §10.3).

Anything an operator needs to see goes into ``events`` explicitly via
this helper. stdout logging is debug output for when you're already
SSH'd in; if that split isn't enforced, useful signal ends up only in
container logs where the UI can't reach it.
"""

from __future__ import annotations

import logging
import uuid
from typing import Any

from sqlalchemy.orm import Session

from core.db.models import Event

logger = logging.getLogger(__name__)


def write_event(
    session: Session,
    level: str,
    stage: str,
    message: str,
    *,
    doc_id: uuid.UUID | None = None,
    shard_idx: int | None = None,
    code: str | None = None,
    context: dict[str, Any] | None = None,
    worker_id: str | None = None,
) -> Event:
    row = Event(
        doc_id=doc_id,
        shard_idx=shard_idx,
        level=level,
        stage=stage,
        code=code,
        message=message,
        context=context or {},
        worker_id=worker_id,
    )
    session.add(row)
    # Deliberately not committed here: callers write events inside their
    # own transaction so an event never describes a state change that
    # failed to commit.
    return row
