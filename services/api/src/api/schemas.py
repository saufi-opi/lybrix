"""Pydantic response/request schemas (PRD §9)."""

from __future__ import annotations

import uuid
from datetime import datetime

from pydantic import BaseModel, Field

from core.db.models import DocState


class PresignRequest(BaseModel):
    collection_id: str
    byte_size: int = Field(gt=0)


class PresignResponse(BaseModel):
    doc_id: uuid.UUID
    upload_url: str


class CommitRequest(BaseModel):
    collection_id: str
    content_sha256: str = Field(min_length=64, max_length=64)
    title: str | None = None
    author: str | None = None
    metadata: dict = Field(default_factory=dict)


class DocumentOut(BaseModel):
    id: uuid.UUID
    collection_id: str | None
    title: str | None
    author: str | None
    page_count: int | None
    byte_size: int | None
    state: DocState
    total_shards: int | None
    shards_done: int
    shards_failed: int
    completeness: float | None
    error_code: str | None
    created_at: datetime
    updated_at: datetime


class RetryRequest(BaseModel):
    scope: str = Field(pattern="^(shards|embed|full)$")


class CollectionCreate(BaseModel):
    id: str = Field(min_length=1, max_length=64)
    name: str
    embedding_model: str = "BAAI/bge-m3"
    vector_dim: int = 1024


class SearchRequest(BaseModel):
    query: str = Field(min_length=1)
    collection: str | None = None
    top_k: int = Field(default=8, ge=1, le=25)


class SystemHealth(BaseModel):
    status: str
    postgres: str
    redis: str
    qdrant: str
    tei_query: str
