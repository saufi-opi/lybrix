"""Collection endpoints (PRD §9).

Embedding model and dimension are set at creation and immutable (§8.1) —
changing a model means creating a new collection and running a re-embed
migration (scripts/reembed.py).
"""

from __future__ import annotations

from core.db.models import Chunk, Collection, Document
from fastapi import APIRouter, Depends, HTTPException
from sqlalchemy import select
from sqlalchemy.orm import Session

from api.deps import get_session, require_scope
from api.schemas import CollectionCreate

router = APIRouter(prefix="/v1/collections", tags=["collections"])


@router.get("")
def list_collections(
    key=Depends(require_scope("search")),
    session: Session = Depends(get_session),
):
    return list(session.execute(select(Collection)).scalars())


@router.post("", status_code=201)
def create_collection(
    body: CollectionCreate,
    key=Depends(require_scope("admin")),
    session: Session = Depends(get_session),
):
    existing = session.get(Collection, body.id)
    if existing is not None:
        raise HTTPException(status_code=409, detail="collection id already exists")
    col = Collection(
        id=body.id,
        name=body.name,
        embedding_model=body.embedding_model,
        vector_dim=body.vector_dim,
    )
    session.add(col)
    return col


@router.get("/{collection_id}/stats")
def collection_stats(
    collection_id: str,
    key=Depends(require_scope("search")),
    session: Session = Depends(get_session),
):
    """Per-collection: doc count, chunk count (§8.1 Collections)."""
    col = session.get(Collection, collection_id)
    if col is None:
        raise HTTPException(status_code=404, detail="collection not found")
    docs = session.execute(
        select(Document).where(Document.collection_id == collection_id)
    ).scalars().all()
    doc_ids = [d.id for d in docs]
    chunk_count = 0
    if doc_ids:
        from sqlalchemy import func

        chunk_count = session.execute(
            select(func.count()).select_from(Chunk).where(Chunk.doc_id.in_(doc_ids))
        ).scalar_one()
    return {
        "id": col.id,
        "name": col.name,
        "embedding_model": col.embedding_model,
        "doc_count": len(docs),
        "chunk_count": chunk_count,
    }
