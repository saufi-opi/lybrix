"""Document/shard/chunk repositories (PRD §5, §6).

All control-plane writes funnel through here so the state machine —
which transitions are legal, when leases are taken, how atomic counters
bump — is testable in one place.
"""

from __future__ import annotations

import uuid
from datetime import UTC, datetime, timedelta

from sqlalchemy import select, update
from sqlalchemy.orm import Session

from core.config import Settings, get_settings
from core.db.models import Chunk, DocState, Document, Shard, ShardState


def get_document(session: Session, doc_id: uuid.UUID) -> Document | None:
    return session.get(Document, doc_id)


def find_duplicate(
    session: Session, collection_id: str, content_sha256: str
) -> Document | None:
    """Upload dedupe: content_sha256 unique per collection (PRD §5)."""
    stmt = select(Document).where(
        Document.collection_id == collection_id,
        Document.content_sha256 == content_sha256,
    )
    return session.execute(stmt).scalar_one_or_none()


def set_doc_state(
    session: Session,
    doc_id: uuid.UUID,
    state: DocState,
    *,
    error_code: str | None = None,
    error_detail: str | None = None,
) -> None:
    session.execute(
        update(Document)
        .where(Document.id == doc_id)
        .values(state=state, error_code=error_code, error_detail=error_detail)
    )


def insert_shards(
    session: Session,
    doc_id: uuid.UUID,
    bounds: list[tuple[int, int]],
    start_idx: int = 0,
) -> int:
    """Insert one row per shard bound; idx runs from start_idx (retry-ladder
    sub-shards continue after the parent's idx so idx stays unique per doc).
    Returns the count inserted."""
    session.add_all(
        [
            Shard(doc_id=doc_id, idx=start_idx + i, page_start=s, page_end=e)
            for i, (s, e) in enumerate(bounds)
        ]
    )
    return len(bounds)


def next_shard_idx(session: Session, doc_id: uuid.UUID) -> int:
    """Max idx per doc + 1 — where retry-ladder sub-shards continue."""
    from sqlalchemy import func

    return int(
        session.execute(
            select(func.max(Shard.idx)).where(Shard.doc_id == doc_id)
        ).scalar_one()
        or 0
    ) + 1


def skip_shard(session: Session, doc_id: uuid.UUID, idx: int) -> None:
    """Mark a parent shard SKIPPED — replaced by retry-ladder sub-shards;
    the embedder selects state=='done' only, so skipped parents never embed."""
    shard = session.get(Shard, (doc_id, idx))
    if shard is None:
        raise LookupError(f"shard {doc_id}/{idx} not found")
    shard.state = ShardState.SKIPPED


def claim_shard(
    session: Session,
    doc_id: uuid.UUID,
    idx: int,
    worker_id: str,
    settings: Settings | None = None,
) -> Shard | None:
    """Atomic claim (PRD §6.3 step 1): UPDATE ... WHERE state IN
    (pending, failed) RETURNING — skip if zero rows (someone else got it)."""
    s = settings or get_settings()
    lease_until = datetime.now(UTC) + timedelta(seconds=s.shard_lease_seconds)
    stmt = (
        update(Shard)
        .where(
            Shard.doc_id == doc_id,
            Shard.idx == idx,
            Shard.state.in_([ShardState.PENDING, ShardState.FAILED]),
        )
        .values(
            state=ShardState.RUNNING,
            attempts=Shard.attempts + 1,
            worker_id=worker_id,
            lease_until=lease_until,
        )
        .returning(Shard)
    )
    return session.execute(stmt).scalar_one_or_none()


def mark_shard_done(
    session: Session,
    doc_id: uuid.UUID,
    idx: int,
    duration_ms: int,
    peak_rss_mb: int,
    parsed_uri: str,
    needs_ocr: bool = False,
) -> None:
    """Mark done and atomically bump shards_done (PRD §6.3 step 6).

    needs_ocr persists the OCR-gate verdict so per-shard OCR decisions
    stay queryable after the fact (shards.needs_ocr column).
    """
    shard = session.get(Shard, (doc_id, idx))
    if shard is None:
        raise LookupError(f"shard {doc_id}/{idx} not found")
    shard.state = ShardState.DONE
    shard.needs_ocr = needs_ocr
    shard.duration_ms = duration_ms
    shard.peak_rss_mb = peak_rss_mb
    shard.parsed_uri = parsed_uri
    shard.lease_until = None
    session.execute(
        update(Document)
        .where(Document.id == doc_id)
        .values(shards_done=Document.shards_done + 1)
    )


def mark_shard_failed(
    session: Session,
    doc_id: uuid.UUID,
    idx: int,
    error_code: str,
    error_detail: str,
) -> None:
    shard = session.get(Shard, (doc_id, idx))
    if shard is None:
        raise LookupError(f"shard {doc_id}/{idx} not found")
    shard.state = ShardState.FAILED
    shard.error_code = error_code
    shard.error_detail = error_detail
    shard.lease_until = None
    session.execute(
        update(Document)
        .where(Document.id == doc_id)
        .values(shards_failed=Document.shards_failed + 1)
    )


def book_settled(doc: Document) -> bool:
    """True when every shard is done or failed → time to enqueue embed
    (PRD §6.3 step 7)."""
    total = doc.total_shards or 0
    return total > 0 and (doc.shards_done + doc.shards_failed) >= total


def requeue_expired_leases(session: Session) -> int:
    """Janitor Reaper (PRD §6.6): running shards whose lease expired go
    back to pending. This is how a docker kill on a parser recovers."""
    now = datetime.now(UTC)
    stmt = (
        update(Shard)
        .where(Shard.state == ShardState.RUNNING, Shard.lease_until < now)
        .values(state=ShardState.PENDING, worker_id=None, lease_until=None)
    )
    res = session.execute(stmt)
    return int(res.rowcount or 0)


def insert_chunks(session: Session, chunks: list[Chunk]) -> None:
    session.add_all(chunks)
