"""Document endpoints (PRD §6.1, §9).

Uploads are presigned direct-to-MinIO — the API never buffers file
bytes. Backpressure: if the parse backlog exceeds MAX_PARSE_BACKLOG,
commit returns 429 with Retry-After (§6.1).
"""

from __future__ import annotations

import hashlib
import tempfile
import uuid
from pathlib import Path

from core.config import get_settings
from core.db import repo
from core.db.models import DocState, Document, Shard
from core.queue import contracts, streams
from core.storage import s3
from fastapi import APIRouter, Depends, HTTPException, Query
from redis import Redis
from sqlalchemy import select
from sqlalchemy.orm import Session

from api.deps import get_session, require_scope
from api.schemas import (
    CommitRequest,
    DocumentOut,
    PresignRequest,
    PresignResponse,
    RetryRequest,
    ShardOut,
)

router = APIRouter(prefix="/v1/documents", tags=["documents"])


def _redis() -> Redis:
    return streams.make_redis()


def _verify_raw_object(s3c, bucket: str, key: str, client_sha: str, max_pages: int) -> None:
    """Stream the raw object once: sha256 + magic bytes + page cap (R-15/R-21).

    PRD §6.1: the API — not the client — owns the dedupe key; §11: reject
    non-PDFs and over-cap books at the door rather than inside a parser.
    Raises HTTPException; the temp file lives only for the pdfium probe."""
    import pypdfium2 as pdfium
    from botocore.exceptions import ClientError

    digest = hashlib.sha256()
    with tempfile.NamedTemporaryFile(suffix=".pdf") as tmp:
        try:
            obj = s3c.get_object(Bucket=bucket, Key=key)
        except ClientError as exc:
            # a skipped PUT reads as a client error, not a 500
            raise HTTPException(status_code=400, detail="object not uploaded") from exc
        stream = obj["Body"]
        head = stream.read(5)
        digest.update(head)
        if head != b"%PDF-":
            raise HTTPException(status_code=400, detail="uploaded object is not a PDF")
        tmp.write(head)
        for chunk in stream.iter_chunks():
            digest.update(chunk)
            tmp.write(chunk)
        tmp.flush()
        if digest.hexdigest() != client_sha:
            raise HTTPException(status_code=400, detail="content_sha256 mismatch")
        try:
            page_count = len(pdfium.PdfDocument(Path(tmp.name)))
        except Exception as exc:
            raise HTTPException(status_code=400, detail=f"unreadable PDF: {exc}") from exc
        if page_count > max_pages:
            raise HTTPException(
                status_code=400,
                detail=f"{page_count} pages over cap {max_pages}",
            )


@router.post("/presign", response_model=PresignResponse)
def presign(
    body: PresignRequest,
    key=Depends(require_scope("ingest")),
    session: Session = Depends(get_session),
):
    """Step 1: client asks for a presigned URL, then PUTs bytes to MinIO."""
    doc_id = uuid.uuid4()
    url = s3.presign_put(s3.make_s3(), get_settings().s3_bucket_raw, s3.raw_key(doc_id))
    return PresignResponse(doc_id=doc_id, upload_url=url)


@router.post("/{doc_id}/commit", status_code=202)
def commit(
    doc_id: uuid.UUID,
    body: CommitRequest,
    key=Depends(require_scope("ingest")),
    session: Session = Depends(get_session),
):
    """Step 3: after the PUT, register the document and enqueue the split.

    409 on duplicate (dedupe by sha256 within a collection), 429 when the
    parse backlog is over the cap (§6.1 backpressure)."""
    r = _redis()
    backlog = streams.queue_depth(r, streams.STREAM_PARSE)
    if backlog > get_settings().max_parse_backlog:
        raise HTTPException(
            status_code=429,
            detail=f"parse backlog {backlog} over cap; retry later",
            headers={"Retry-After": "60"},
        )

    if repo.find_duplicate(session, body.collection_id, body.content_sha256) is not None:
        raise HTTPException(status_code=409, detail="duplicate document in collection")

    # One streaming pass serves all three checks (R-15/R-21): the verified
    # hash below is client-supplied but only reaches the row when it equals
    # the server-computed value — otherwise the request 400s.
    raw_key_str = s3.raw_key(doc_id)
    _verify_raw_object(
        s3.make_s3(),
        get_settings().s3_bucket_raw,
        raw_key_str,
        body.content_sha256,
        get_settings().max_document_pages,
    )

    doc = Document(
        id=doc_id,
        collection_id=body.collection_id,
        title=body.title,
        author=body.author,
        source_uri=f"s3://{get_settings().s3_bucket_raw}/{s3.raw_key(doc_id)}",
        content_sha256=body.content_sha256,
        state=DocState.UPLOADED,
        uploaded_by=key.name,
        doc_metadata=body.metadata,
    )
    session.add(doc)
    session.flush()
    streams.xadd_job(r, streams.STREAM_SPLIT, contracts.SplitJob(doc_id=doc_id, source_uri=doc.source_uri))
    return {"id": str(doc_id), "state": doc.state.value}


@router.get("", response_model=list[DocumentOut])
def list_documents(
    state: DocState | None = None,
    collection: str | None = None,
    q: str | None = None,
    limit: int = Query(default=50, le=200),
    offset: int = 0,
    key=Depends(require_scope("search")),
    session: Session = Depends(get_session),
):
    stmt = select(Document).order_by(Document.updated_at.desc()).limit(limit).offset(offset)
    if state:
        stmt = stmt.where(Document.state == state)
    if collection:
        stmt = stmt.where(Document.collection_id == collection)
    if q:
        stmt = stmt.where(Document.title.ilike(f"%{q}%"))
    return list(session.execute(stmt).scalars())


@router.get("/{doc_id}", response_model=DocumentOut)
def get_document(
    doc_id: uuid.UUID,
    key=Depends(require_scope("search")),
    session: Session = Depends(get_session),
):
    doc = repo.get_document(session, doc_id)
    if doc is None:
        raise HTTPException(status_code=404, detail="document not found")
    return doc


@router.get("/{doc_id}/shards", response_model=list[ShardOut])
def get_shards(
    doc_id: uuid.UUID,
    key=Depends(require_scope("search")),
    session: Session = Depends(get_session),
):
    """Shard rows for the admin UI's shard grid (§8.1)."""
    stmt = select(Shard).where(Shard.doc_id == doc_id).order_by(Shard.idx)
    return [
        {
            "idx": s.idx,
            "page_start": s.page_start,
            "page_end": s.page_end,
            "state": s.state.value,
            "attempts": s.attempts,
            "needs_ocr": s.needs_ocr,
            "duration_ms": s.duration_ms,
            "peak_rss_mb": s.peak_rss_mb,
            "error_code": s.error_code,
        }
        for s in session.execute(stmt).scalars()
    ]


@router.post("/{doc_id}/retry")
def retry(
    doc_id: uuid.UUID,
    body: RetryRequest,
    key=Depends(require_scope("admin")),
    session: Session = Depends(get_session),
):
    """Three retry scopes cost very different amounts (§8.3): shards =
    seconds, embed = ~30s reusing parsed JSON, full = minutes from source."""
    doc = repo.get_document(session, doc_id)
    if doc is None:
        raise HTTPException(status_code=404, detail="document not found")
    r = _redis()
    if body.scope == "embed":
        repo.set_doc_state(session, doc_id, DocState.EMBEDDING)
        streams.xadd_job(r, streams.STREAM_EMBED, contracts.EmbedJob(doc_id=doc_id))
    elif body.scope == "full":
        repo.set_doc_state(session, doc_id, DocState.SPLITTING)
        streams.xadd_job(r, streams.STREAM_SPLIT, contracts.SplitJob(doc_id=doc_id, source_uri=doc.source_uri))
    else:  # shards: requeue failed shards only
        failed_shards = (
            session.execute(
                select(Shard).where(Shard.doc_id == doc_id, Shard.state == "failed").order_by(Shard.idx)
            )
            .scalars()
            .all()
        )
        if not failed_shards:
            raise HTTPException(status_code=409, detail="no failed shards to retry")
        session.execute(
            Shard.__table__.update()
            .where(Shard.doc_id == doc_id, Shard.state == "failed")
            .values(state="pending", error_code=None, error_detail=None)
        )
        # Counter hygiene: mark_shard_failed bumped shards_failed per failure;
        # requeueing undoes those failures. Without this the embedder would
        # compute a wrong completeness and book_settled could double-count.
        session.execute(
            Document.__table__.update()
            .where(Document.id == doc_id)
            .values(shards_failed=Document.shards_failed - len(failed_shards))
        )
        # Back to PARSING so the janitor's settled-book sweep and stuck-doc
        # warnings see this book again (a stranded pending shard in a
        # terminal-state doc is invisible to every recovery path — R-11).
        repo.set_doc_state(session, doc_id, DocState.PARSING)
        for shard in failed_shards:
            streams.xadd_job(
                r,
                streams.STREAM_PARSE,
                contracts.ParseJob(
                    doc_id=doc_id,
                    idx=shard.idx,
                    page_start=shard.page_start,
                    page_end=shard.page_end,
                    source_uri=doc.source_uri,
                ),
            )
    return {"id": str(doc_id), "retried": body.scope}
