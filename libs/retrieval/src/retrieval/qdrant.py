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


def _raw_rest(client: QdrantClient, method: str, url: str, json: dict | None = None) -> dict:
    """Raw REST through the client's typed-openapi transport.

    Verified against qdrant-client 1.19.0: the typed CollectionInfo model
    silently drops fields the generated models don't know, so config reads
    that must see every key go through the ApiClient.request passthrough
    (auth headers and base URL are carried).
    """
    return client.http.client.request(type_=dict, method=method, url=url, json=json)


def _sparse_config(client: QdrantClient, name: str) -> dict:
    """The collection's sparse_vectors config via raw REST GET."""
    body = _raw_rest(client, "GET", f"/collections/{name}")
    params = ((body.get("result") or {}).get("config") or {}).get("params") or {}
    return params.get("sparse_vectors") or {}


def ensure_bm25_idf(client: QdrantClient, settings: Settings | None = None) -> str:
    """Idempotent: ensure the `bm25` sparse space uses modifier=idf.

    With modifier=idf the SERVER multiplies in IDF at query time, so the
    client only writes TF-based sparse vectors (retrieval.bm25.encode_bm25).
    Read-only inspection first (raw REST GET — the typed CollectionInfo
    model drops config fields); PATCH {"sparse_vectors": {"bm25":
    {"modifier": "idf"}}} only when the modifier differs, so re-runs are
    no-ops and never touch vectors or other config. Verified live: this
    PATCH works on qdrant 1.19.1 (GET then shows modifier=idf).
    """
    name = _collection(settings)
    current = (_sparse_config(client, name).get("bm25") or {}).get("modifier")
    if current == "idf":
        return name  # already applied: no request sent
    _raw_rest(
        client, "PATCH", f"/collections/{name}", json={"sparse_vectors": {"bm25": {"modifier": "idf"}}}
    )
    return name


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
    ensure_bm25_idf(client, s)
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
