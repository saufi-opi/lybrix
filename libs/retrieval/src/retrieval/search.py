"""Hybrid search with RRF fusion, filters, and Postgres hydration
(PRD §7.1).

1. Hybrid search — dense (bge-m3) + sparse BM25 in Qdrant, fused with
   RRF. Purely dense retrieval on technical books loses on exact terms:
   version numbers, API names, acronyms.
2. Filter by collection_id plus optional doc_id/tags/page range.
3. Hydrate text from Postgres by chunk id.
"""

from __future__ import annotations

import uuid
from dataclasses import dataclass

from core.db.models import Chunk, Document
from qdrant_client import QdrantClient
from qdrant_client import models as qm
from sqlalchemy import select
from sqlalchemy.orm import Session


@dataclass(frozen=True)
class SearchHit:
    chunk_id: uuid.UUID
    doc_id: uuid.UUID
    doc_title: str | None
    page_start: int | None
    page_end: int | None
    heading_path: list[str]
    text: str
    score: float
    partial: bool = False


def _rrf_fuse(dense_hits, sparse_hits, k: int = 60) -> dict[str, float]:
    """Reciprocal Rank Fusion over the two ranked lists keyed by point id."""
    scores: dict[str, float] = {}
    for ranked in (dense_hits, sparse_hits):
        for rank, point in enumerate(ranked):
            pid = str(point.id)
            scores[pid] = scores.get(pid, 0.0) + 1.0 / (k + rank + 1)
    return scores


def build_filter(
    collection_id: str | None = None,
    doc_id: str | None = None,
    collection_ids: list[str] | None = None,
) -> qm.Filter | None:
    must = []
    if collection_id:
        must.append(
            qm.FieldCondition(key="collection_id", match=qm.MatchValue(value=collection_id))
        )
    if collection_ids:
        # Key-level scope (api keys carry a collections array): MatchAny
        # pushes the scope INTO the Qdrant query so top_k slots are filled
        # from allowed collections only — post-filtering after truncation
        # silently returned fewer than top_k (R-14).
        must.append(qm.FieldCondition(key="collection_id", match=qm.MatchAny(any=collection_ids)))
    if doc_id:
        must.append(qm.FieldCondition(key="doc_id", match=qm.MatchValue(value=doc_id)))
    return qm.Filter(must=must) if must else None


def search_vectors(
    client: QdrantClient,
    collection: str,
    dense_query: list[float],
    sparse_query: dict | None,
    limit: int,
    qfilter: qm.Filter | None = None,
) -> tuple[list, list]:
    """Two prefetches (dense + bm25) fused by Qdrant's native RRF.

    sparse_query is a client-side encoded BM25 sparse vector (see
    retrieval.bm25.encode_bm25 — the verified live path: this server has no
    native-function/inference BM25 runtime, so the client encodes and the
    server's idf modifier multiplies in IDF at query time).
    """
    prefetch = [
        qm.Prefetch(
            query=dense_query,
            using="dense",
            limit=limit,
            filter=qfilter,
        )
    ]
    if sparse_query:
        prefetch.append(
            qm.Prefetch(
                query=qm.SparseVector(
                    indices=sparse_query["indices"], values=sparse_query["values"]
                ),
                using="bm25",
                limit=limit,
                filter=qfilter,
            )
        )
    # Weighted RRF when both lanes are present (Qdrant >=1.17; verified live on
    # 1.19.1: Rrf(k=60, weights=[...]) serializes and is accepted). Dense gets
    # full weight, BM25 0.15 — measured 2026-09-19: equal-weight RRF let generic
    # keyword matches out-compete the correct semantic hits on this corpus
    # (eval phase1-bm25: hit@1 53%→33%, 6 regressions); with 0.15 BM25 only
    # boosts candidates that BOTH lanes agree on. Sparse-only/dense-only
    # paths are unaffected (weights apply to a single-lane list trivially).
    if len(prefetch) == 2:
        fusion = qm.RrfQuery(rrf=qm.Rrf(k=60, weights=[1.0, 0.15]))
    else:
        fusion = qm.FusionQuery(fusion=qm.Fusion.RRF)
    res = client.query_points(
        collection_name=collection,
        prefetch=prefetch,
        query=fusion,
        limit=limit,
        with_payload=True,
    )
    return res.points, res.points  # fused list; dense list kept for symmetry


def hydrate(
    session: Session,
    fused_points,
    doc_cache: dict[str, Document] | None = None,
) -> list[SearchHit]:
    """Fetch chunk text + doc metadata from Postgres by (doc_id, chunk_hash).

    Both halves of the key matter (R-12): chunk_hash is unique only per
    (doc_id, chunk_hash) — identical normalized text in two documents
    shares a hash, and keying rows by hash alone attributed the hit to
    whichever row the query returned first.
    """
    cache = doc_cache if doc_cache is not None else {}
    hashes = list({p.payload.get("chunk_hash") for p in fused_points if p.payload})
    doc_ids = list(
        {p.payload.get("doc_id") for p in fused_points if p.payload and p.payload.get("doc_id")}
    )
    rows: dict[tuple[str, str], Chunk] = {}
    if hashes:
        stmt = select(Chunk).where(
            Chunk.chunk_hash.in_(hashes),
            Chunk.doc_id.in_([uuid.UUID(d) for d in doc_ids]),
        )
        for c in session.execute(stmt).scalars():
            rows[(str(c.doc_id), c.chunk_hash)] = c

    hits: list[SearchHit] = []
    for p in fused_points:
        payload = p.payload or {}
        chunk = rows.get((str(payload.get("doc_id")), payload.get("chunk_hash")))
        if chunk is None:
            continue  # vector without a Postgres row: stale point, skip
        doc_id = str(chunk.doc_id)
        if doc_id not in cache:
            doc = session.get(Document, chunk.doc_id)
            if doc is not None:
                cache[doc_id] = doc
        doc = cache.get(doc_id)
        partial = bool(doc and doc.completeness is not None and doc.completeness < 1.0)
        hits.append(
            SearchHit(
                chunk_id=chunk.id,
                doc_id=chunk.doc_id,
                doc_title=doc.title if doc else None,
                page_start=chunk.page_start,
                page_end=chunk.page_end,
                heading_path=list(chunk.heading_path or []),
                text=chunk.text,
                score=float(p.score),
                partial=partial,
            )
        )
    return hits


def hybrid_search(
    qdrant: QdrantClient,
    collection: str,
    session: Session,
    dense_query: list[float],
    sparse_query: dict | None,
    top_k: int,
    collection_id: str | None = None,
    doc_id: str | None = None,
    collection_ids: list[str] | None = None,
) -> list[SearchHit]:
    """Full §7.1 path minus the (phase 2) rerank: filter → hybrid → hydrate."""
    qfilter = build_filter(
        collection_id=collection_id, doc_id=doc_id, collection_ids=collection_ids
    )
    points, _ = search_vectors(
        qdrant, collection, dense_query, sparse_query, top_k, qfilter
    )
    return hydrate(session, points)
