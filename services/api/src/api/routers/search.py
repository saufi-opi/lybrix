"""Search endpoint (PRD §9, §7.1): dense+BM25 hybrid with RRF fusion.

The query embedding goes to tei-query — NEVER tei-ingest (§4.3): an
8-token query queued behind a 65k-token ingest batch is the easiest way
to destroy p99 latency.
"""

from __future__ import annotations

from core.config import get_settings
from embedding.client import TeiClient, TeiUnavailable
from fastapi import APIRouter, Depends, HTTPException
from qdrant_client import QdrantClient
from retrieval import search as rs
from retrieval.qdrant import COLLECTION_NAME
from sqlalchemy.orm import Session

from api.deps import get_session, require_scope
from api.schemas import SearchRequest

router = APIRouter(prefix="/v1/search", tags=["search"])


def _bm25_stub(query: str) -> dict | None:
    """v1 runs the BM25 side via Qdrant's idf-bearing sparse vectors at
    query time; until the sparse-embedding path is wired (M3), dense-only
    retrieval is served and sparse stays None."""
    return None


@router.post("")
def search(
    body: SearchRequest,
    key=Depends(require_scope("search")),
    session: Session = Depends(get_session),
):
    s = get_settings()
    with TeiClient(s.tei_query_url) as qclient:
        try:
            dense = qclient.embed([f"{s.embed_query_prefix}{body.query}"])[0]
        except TeiUnavailable as exc:
            raise HTTPException(status_code=503, detail=str(exc)) from exc

    qdrant = QdrantClient(url=s.qdrant_url, api_key=s.qdrant_api_key, timeout=5)
    key_scope = list(getattr(key, "collections", None) or [])
    try:
        hits = rs.hybrid_search(
            qdrant,
            COLLECTION_NAME,
            session,
            dense_query=dense,
            sparse_query=_bm25_stub(body.query),
            top_k=min(body.top_k, s.search_max_top_k),
            collection_id=body.collection,
            collection_ids=key_scope or None,
        )
    finally:
        qdrant.close()
    # key-level scope is pushed into the Qdrant filter (R-14) — no
    # post-filter here: filtering after top_k truncation returned fewer
    # than top_k results for scoped keys.

    return [
        {
            "chunk_id": str(h.chunk_id),
            "doc_id": str(h.doc_id),
            "doc_title": h.doc_title,
            "page_start": h.page_start,
            "page_end": h.page_end,
            "heading_path": h.heading_path,
            "text": h.text,
            "score": h.score,
            "partial": h.partial,
        }
        for h in hits
    ]
