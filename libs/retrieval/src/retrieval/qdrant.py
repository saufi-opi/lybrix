"""Qdrant collection management and upserts (PRD §5.3).

Point id = chunk_hash. Payload deliberately does NOT contain the chunk
text — the MCP server hydrates text from Postgres by id after the
vector search. This keeps the HNSW index in RAM comfortably.
"""

from __future__ import annotations

import hashlib
import uuid
from typing import Any

from core.config import Settings, get_settings
from qdrant_client import QdrantClient
from qdrant_client import models as qm

COLLECTION_NAME = "chunks"

# Native BM25 function (Qdrant >= 1.19): server-side sparse generation from
# the "text" payload field, written to the existing "bm25" sparse space.
# The old client-side design declared this sparse config but never wrote
# to it (zero points carry bm25 vectors), so reusing the name is safe.
BM25_FUNCTION_NAME = "bm25"
TEXT_FIELD = "text"


def _bm25_function() -> dict:
    return {
        "name": BM25_FUNCTION_NAME,
        "function_type": "bm25",
        "input": [{"type": "text", "text_field": TEXT_FIELD}],
        "output": BM25_FUNCTION_NAME,
    }


def _raw_rest(client: QdrantClient, method: str, url: str, json: dict | None = None) -> dict:
    """Raw REST through the client's typed-openapi transport.

    Verified against qdrant-client 1.19.0: the typed CreateCollection /
    UpdateCollection models have no `functions` field and unknown kwargs are
    asserted-rejected, so native-function create/patch MUST go through the
    ApiClient.request passthrough (auth headers and base URL are carried).
    """
    return client.http.client.request(type_=dict, method=method, url=url, json=json)


def _collection_functions(client: QdrantClient, name: str) -> list[dict] | None:
    """Functions array from GET /collections/{name} via raw REST.

    Verified against qdrant-client 1.19.0: the typed CollectionInfo model
    silently DROPS the `functions` field, so the raw body is the only source.
    """
    body = _raw_rest(client, "GET", f"/collections/{name}")
    return (body.get("result") or {}).get("functions")


def ensure_bm25_function(client: QdrantClient, settings: Settings | None = None) -> str:
    """Idempotent: add the native BM25 function to an existing collection.

    Read-only inspection first (GET /collections/{name} — raw REST, because
    the typed CollectionInfo model drops the functions field); patch via
    PATCH /collections/{name} with {"functions": [...]} only when the
    function is absent or its input/output differ, so re-runs are no-ops and
    never disturb vectors or other config.
    """
    name = _collection(settings)
    functions = _collection_functions(client, name) or []
    desired = _bm25_function()
    for function in functions:
        if function.get("name") == BM25_FUNCTION_NAME:
            if function.get("output") == desired["output"] and function.get(
                "input"
            ) == desired["input"]:
                return name  # already present and matching: no request sent
            break
    _raw_rest(client, "PATCH", f"/collections/{name}", json={"functions": [desired]})
    return name


def _collection(settings: Settings | None = None) -> str:
    return COLLECTION_NAME


def ensure_collection(client: QdrantClient, settings: Settings | None = None) -> str:
    """Create the chunks collection (idempotent) with bge-m3 dense vectors,
    a BM25 sparse vector for hybrid search, and int8 always-ram
    quantization per PRD §5.3."""
    s = settings or get_settings()
    name = _collection(settings)
    if client.collection_exists(name):
        # Existing-collection path (the one that matters in prod): make sure
        # the native BM25 function is present, idempotent-by-inspection.
        return ensure_bm25_function(client, s)

    # qdrant-client 1.19.0: the typed CreateCollection model has no
    # `functions` field and unknown kwargs are asserted-rejected, so the
    # function definition rides on the create body via raw REST.
    _raw_rest(
        client,
        "PUT",
        f"/collections/{name}",
        json={
            "vectors": {"dense": {"size": s.embed_dim, "distance": "Cosine"}},
            "sparse_vectors": {
                "bm25": {"on_disk": False, "full_scan_threshold": 1000}
            },
            "hnsw_config": {"m": 16, "ef_construct": 128},
            "quantization_config": {
                "scalar": {"type": "int8", "always_ram": True}
            },
            "functions": [_bm25_function()],
        },
    )
    client.update_collection(
        collection_name=name,
        payload_schema=None,  # payload indexes created below
    )
    # Payload indexes for filtered hybrid search (PRD §7.1 step 2).
    import contextlib

    for field, kind in (
        ("doc_id", qm.PayloadSchemaType.KEYWORD),
        ("collection_id", qm.PayloadSchemaType.KEYWORD),
        ("page_start", qm.PayloadSchemaType.INTEGER),
    ):
        with contextlib.suppress(Exception):  # index already exists
            client.create_payload_index(name, field_name=field, field_schema=kind)
    return name


def point_id_for(chunk_hash: str) -> str:
    """Qdrant accepts UUIDs or unsigned ints as point ids; derive a stable
    UUID5 from the chunk hash so re-embedding upserts, never duplicates."""
    return str(uuid.uuid5(uuid.NAMESPACE_URL, f"chunk:{chunk_hash}"))


def upsert_chunks(
    client: QdrantClient,
    points: list[dict[str, Any]],
    settings: Settings | None = None,
) -> int:
    """Upsert embedded chunks: {chunk_hash, doc_id, collection_id, vector,
    page_start, page_end, heading_path}."""
    if not points:
        return 0
    s = settings or get_settings()
    name = _collection(s)
    qpoints = [
        qm.PointStruct(
            id=point_id_for(p["chunk_hash"]),
            vector={"dense": p["vector"]},
            payload={
                "doc_id": str(p["doc_id"]),
                "collection_id": p.get("collection_id", ""),
                "chunk_hash": p["chunk_hash"],
                "page_start": p.get("page_start"),
                "page_end": p.get("page_end"),
                "heading_path": list(p.get("heading_path") or []),
            },
        )
        for p in points
    ]
    client.upsert(collection_name=name, points=qpoints, wait=True)
    return len(qpoints)


def delete_doc_points(client: QdrantClient, doc_id: str) -> None:
    client.delete(
        collection_name=_collection(),
        points_selector=qm.FilterSelector(
            filter=qm.Filter(
                must=[qm.FieldCondition(key="doc_id", match=qm.MatchValue(value=doc_id))]
            )
        ),
    )


def hash_text(text: str) -> str:
    return hashlib.sha256(" ".join(text.split()).encode("utf-8")).hexdigest()
