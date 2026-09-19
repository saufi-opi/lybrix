"""FastMCP server: the six tools from PRD §7.2.

Design rules implemented here (§7.2):
  * every result carries the citation triple (doc_title, page_start, page_end);
  * every response is bounded — read_pages caps at READ_PAGES_MAX pages,
    search caps top_k at SEARCH_MAX_TOP_K;
  * tool descriptions state WHEN to use each one, not just what it does;
  * search flags partial: true when a matched document has completeness
    < 1.0, so the agent knows the corpus has a hole there.

Heavy clients (Qdrant, TEI, DB engine) are built lazily inside
build_server() so importing and unit-testing the *_impl functions never
requires running infrastructure — books-rag's mcp_server convention.
"""

from __future__ import annotations

import logging
import uuid
from collections.abc import Callable
from typing import Any

from core.config import Settings, get_settings
from core.db.models import Chunk, DocState, Document
from core.db.session import make_engine, make_session_factory
from sqlalchemy import func, select

from mcp_server.auth import assert_collection_allowed

logger = logging.getLogger(__name__)


def _clamp_top_k(settings: Settings, top_k: int) -> int:
    if top_k < 1:
        raise ValueError("top_k must be >= 1")
    return min(top_k, settings.search_max_top_k)


def search_impl(
    dbsession,
    qdrant,
    query_embedder: Callable[[str], list[float]],
    settings: Settings,
    query: str,
    collection: str | None = None,
    top_k: int = 8,
) -> list[dict[str, Any]]:
    """Business logic behind the `search` tool — unit-testable without a
    running Qdrant: pass fakes for qdrant/embedder."""
    from retrieval.qdrant import COLLECTION_NAME
    from retrieval.search import hybrid_search

    limit = _clamp_top_k(settings, top_k)
    dense = query_embedder(f"search_query: {query}")
    use_bm25 = settings.retrieval_bm25_enabled
    from retrieval.bm25 import encode_bm25

    hits = hybrid_search(
        qdrant,
        COLLECTION_NAME,
        dbsession,
        dense_query=dense,
        # flag on: client-side BM25 sparse query (server idf modifier applies
        # at query time); flag off: None → byte-identical dense-only path
        sparse_query=encode_bm25(query) if use_bm25 else None,
        top_k=limit,
        collection_id=collection,
    )
    out = []
    for h in hits:
        item = {
            "doc_title": h.doc_title,
            "page_start": h.page_start,
            "page_end": h.page_end,
            "heading_path": h.heading_path,
            "text": h.text,
            "score": h.score,
            "doc_id": str(h.doc_id),
            "chunk_id": str(h.chunk_id),
        }
        if h.partial:
            item["partial"] = True
            item["note"] = (
                "source document parsed with holes (completeness < 1.0); "
                "nearby pages may be missing"
            )
        out.append(item)
    return out


def list_collections_impl(dbsession) -> list[dict[str, Any]]:
    from core.db.models import Collection

    rows = dbsession.execute(select(Collection)).scalars().all()
    out = []
    for c in rows:
        doc_count = dbsession.execute(
            select(func.count()).select_from(Document).where(Document.collection_id == c.id)
        ).scalar_one()
        out.append(
            {"id": c.id, "name": c.name, "embedding_model": c.embedding_model, "doc_count": doc_count}
        )
    return out


def list_documents_impl(
    dbsession,
    collection: str | None = None,
    state: str | None = None,
    query: str | None = None,
    limit: int = 50,
) -> list[dict[str, Any]]:
    stmt = select(Document).order_by(Document.updated_at.desc()).limit(min(limit, 200))
    if collection:
        stmt = stmt.where(Document.collection_id == collection)
    if state:
        stmt = stmt.where(Document.state == DocState(state))
    if query:
        stmt = stmt.where(Document.title.ilike(f"%{query}%"))
    return [
        {
            "id": str(d.id),
            "title": d.title,
            "author": d.author,
            "page_count": d.page_count,
            "state": d.state.value,
            "completeness": float(d.completeness) if d.completeness is not None else None,
        }
        for d in dbsession.execute(stmt).scalars()
    ]


def get_document_impl(dbsession, doc_id: str) -> dict[str, Any] | None:
    doc = dbsession.get(Document, uuid.UUID(doc_id))
    if doc is None:
        return None
    chunk_count = dbsession.execute(
        select(func.count()).select_from(Chunk).where(Chunk.doc_id == doc.id)
    ).scalar_one()
    return {
        "id": str(doc.id),
        "title": doc.title,
        "author": doc.author,
        "state": doc.state.value,
        "page_count": doc.page_count,
        "completeness": float(doc.completeness) if doc.completeness is not None else None,
        "chunk_count": chunk_count,
        "metadata": doc.doc_metadata,
    }


def read_pages_impl(
    dbsession, doc_id: str, page_start: int, page_end: int, settings: Settings
) -> dict[str, Any]:
    """Bounded page read (§7.2: cap at READ_PAGES_MAX pages/call)."""
    doc = dbsession.get(Document, uuid.UUID(doc_id))
    if doc is None:
        raise ValueError(f"document {doc_id} not found")
    page_start = max(1, page_start)
    page_end = min(page_end, page_start + settings.read_pages_max - 1)
    if doc.page_count:
        page_end = min(page_end, doc.page_count)

    rows = (
        dbsession.execute(
            select(Chunk)
            .where(Chunk.doc_id == doc.id, Chunk.page_start >= page_start, Chunk.page_start <= page_end)
            .order_by(Chunk.seq)
        )
        .scalars()
        .all()
    )
    return {
        "doc_title": doc.title,
        "page_start": page_start,
        "page_end": page_end,
        "markdown": "\n\n".join(r.text for r in rows),
        "truncated_to": settings.read_pages_max,
    }


def get_chunk_context_impl(dbsession, chunk_id: str, window: int = 2) -> list[dict[str, Any]]:
    """Neighbouring chunks in order (§7.1 step 4) — follow-up after a hit."""
    chunk = dbsession.get(Chunk, uuid.UUID(chunk_id))
    if chunk is None:
        raise ValueError(f"chunk {chunk_id} not found")
    rows = (
        dbsession.execute(
            select(Chunk)
            .where(Chunk.doc_id == chunk.doc_id, Chunk.seq >= chunk.seq - window, Chunk.seq <= chunk.seq + window)
            .order_by(Chunk.seq)
        )
        .scalars()
        .all()
    )
    return [
        {
            "chunk_id": str(r.id),
            "seq": r.seq,
            "page_start": r.page_start,
            "page_end": r.page_end,
            "text": r.text,
        }
        for r in rows
    ]


def build_server(settings: Settings | None = None):
    from fastmcp import FastMCP

    s = settings or get_settings()
    engine = make_engine(s)
    factory = make_session_factory(engine)

    from qdrant_client import QdrantClient

    qdrant = QdrantClient(url=s.qdrant_url, api_key=s.qdrant_api_key, timeout=5)
    from embedding.client import TeiClient

    tei_query = TeiClient(s.tei_query_url)

    from mcp_server.middleware import UsageMiddleware

    mcp = FastMCP("lybrix", middleware=[UsageMiddleware(session_factory=factory)])

    @mcp.tool
    def search(query: str, collection: str | None = None, top_k: int = 8) -> list[dict[str, Any]]:
        """Primary tool. Hybrid (dense + BM25) semantic search over the
        ingested corpus with page-number citations. Use for "what does the
        library say about X" questions; returns top_k chunks with doc
        title, page range, and heading path for verification."""
        from mcp_server.middleware import current_api_key

        key = current_api_key()
        if key is None:
            raise RuntimeError("search called without an authenticated key")
        assert_collection_allowed(key, collection)
        with factory() as session:
            return search_impl(session, qdrant, lambda q: tei_query.embed([q])[0], s, query, collection, top_k)

    @mcp.tool
    def list_collections() -> list[dict[str, Any]]:
        """Enumerate collections (id, name, model, doc count). Call this
        first to scope later searches to a subject area."""
        with factory() as session:
            return list_collections_impl(session)

    @mcp.tool
    def list_documents(
        collection: str | None = None, state: str | None = None, query: str | None = None, limit: int = 50
    ) -> list[dict[str, Any]]:
        """Browse/verify: list documents with state and completeness. Use
        to check whether a specific book is ingested before deep reads."""
        with factory() as session:
            return list_documents_impl(session, collection, state, query, limit)

    @mcp.tool
    def get_document(doc_id: str) -> dict[str, Any] | None:
        """Orientation before deep read: metadata, completeness, chunk
        count for one document."""
        with factory() as session:
            return get_document_impl(session, doc_id)

    @mcp.tool
    def read_pages(doc_id: str, page_start: int, page_end: int) -> dict[str, Any]:
        """Read a bounded page range as markdown (max 30 pages/call). Use
        after search/get_document when a citation needs surrounding
        context; never to scan whole books."""
        with factory() as session:
            return read_pages_impl(session, doc_id, page_start, page_end, s)

    @mcp.tool
    def get_chunk_context(chunk_id: str, window: int = 2) -> list[dict[str, Any]]:
        """Fetch ±window neighbouring chunks in document order. Use after
        a search hit that cut mid-argument."""
        with factory() as session:
            return get_chunk_context_impl(session, chunk_id, window)

    @mcp.custom_route("/health", methods=["GET"])
    async def health(request):
        from starlette.responses import JSONResponse

        # /health bypasses auth middleware; return nothing but status.
        return JSONResponse({"status": "ok"})

    return mcp


def main() -> None:  # pragma: no cover - process entry
    s = get_settings()
    logging.basicConfig(level=s.log_level.upper())
    mcp = build_server(s)
    from starlette.middleware import Middleware as ASGIMiddleware

    from mcp_server.middleware import McpAuthMiddleware

    app = mcp.http_app(
        path="/mcp",
        middleware=[
            ASGIMiddleware(
                McpAuthMiddleware,
                session_factory=make_session_factory(make_engine(s)),
            )
        ],
    )
    import uvicorn

    uvicorn.run(app, host=s.mcp_host, port=s.mcp_port)


if __name__ == "__main__":
    main()
