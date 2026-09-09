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


def _collection(settings: Settings | None = None) -> str:
    return COLLECTION_NAME


def ensure_collection(client: QdrantClient, settings: Settings | None = None) -> str:
    """Create the chunks collection (idempotent) with bge-m3 dense vectors,
    a BM25 sparse vector for hybrid search, and int8 always-ram
    quantization per PRD §5.3."""
    s = settings or get_settings()
    name = _collection(settings)
    if client.collection_exists(name):
        return name

    client.create_collection(
        collection_name=name,
        # qdrant-client >= 1.10: VectorsConfig is a typing.Union — pass the
        # named-vectors dict directly (instantiating the Union raises
        # "Cannot instantiate typing.Union").
        vectors_config={"dense": qm.VectorParams(size=s.embed_dim, distance=qm.Distance.COSINE)},
        sparse_vectors_config={
            "bm25": qm.SparseVectorParams(
                index=qm.SparseIndexParams(on_disk=False, full_scan_threshold=1000)
            )
        },
        hnsw_config=qm.HnswConfigDiff(m=16, ef_construct=128),
        quantization_config=qm.ScalarQuantization(
            scalar=qm.ScalarQuantizationConfig(type=qm.ScalarType.INT8, always_ram=True)
        ),
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
