"""Initial schema: collections, documents, shards, chunks, events, api_keys.

Revision ID: 0001_initial
Revises:
Create Date: 2026-09-08
"""

from __future__ import annotations

import sqlalchemy as sa
from alembic import op
from sqlalchemy.dialects import postgresql as pg

revision: str = "0001_initial"
down_revision: str | None = None
branch_labels: str | None = None
depends_on: str | None = None

DOC_STATES = (
    "uploaded", "splitting", "parsing", "embedding", "indexing",
    "ready", "failed", "partial", "archived",
)
SHARD_STATES = ("pending", "running", "done", "failed", "skipped")


def upgrade() -> None:
    doc_state = pg.ENUM(*DOC_STATES, name="doc_state", create_type=False)
    shard_state = pg.ENUM(*SHARD_STATES, name="shard_state", create_type=False)
    doc_state.create(op.get_bind(), checkfirst=True)
    shard_state.create(op.get_bind(), checkfirst=True)

    op.create_table(
        "collections",
        sa.Column("id", sa.Text(), primary_key=True),
        sa.Column("name", sa.Text(), nullable=False),
        sa.Column("embedding_model", sa.Text(), nullable=False),
        sa.Column("vector_dim", sa.Integer(), nullable=False),
        sa.Column("created_at", sa.DateTime(timezone=True), server_default=sa.func.now()),
    )

    op.create_table(
        "documents",
        sa.Column("id", pg.UUID(as_uuid=True), primary_key=True),
        sa.Column("collection_id", sa.Text(), sa.ForeignKey("collections.id")),
        sa.Column("title", sa.Text()),
        sa.Column("author", sa.Text()),
        sa.Column("source_uri", sa.Text(), nullable=False),
        sa.Column("content_sha256", sa.String(64), nullable=False),
        sa.Column("page_count", sa.Integer()),
        sa.Column("byte_size", sa.BigInteger()),
        sa.Column("state", doc_state, nullable=False, server_default="uploaded"),
        sa.Column("total_shards", sa.Integer()),
        sa.Column("shards_done", sa.Integer(), nullable=False, server_default="0"),
        sa.Column("shards_failed", sa.Integer(), nullable=False, server_default="0"),
        sa.Column("chunk_count", sa.Integer()),
        sa.Column("completeness", sa.Numeric(5, 4)),
        sa.Column("error_code", sa.Text()),
        sa.Column("error_detail", sa.Text()),
        sa.Column("metadata", pg.JSONB(), server_default="{}"),
        sa.Column("uploaded_by", sa.Text()),
        sa.Column("created_at", sa.DateTime(timezone=True), server_default=sa.func.now()),
        sa.Column("updated_at", sa.DateTime(timezone=True), server_default=sa.func.now()),
        sa.Column("ready_at", sa.DateTime(timezone=True)),
        sa.UniqueConstraint("collection_id", "content_sha256", name="uq_doc_content"),
    )
    op.create_index("ix_documents_state", "documents", ["state"])

    op.create_table(
        "shards",
        sa.Column("doc_id", pg.UUID(as_uuid=True), sa.ForeignKey("documents.id", ondelete="CASCADE"), primary_key=True),
        sa.Column("idx", sa.Integer(), primary_key=True),
        sa.Column("page_start", sa.Integer(), nullable=False),
        sa.Column("page_end", sa.Integer(), nullable=False),
        sa.Column("state", shard_state, nullable=False, server_default="pending"),
        sa.Column("attempts", sa.Integer(), nullable=False, server_default="0"),
        sa.Column("needs_ocr", sa.Boolean(), server_default=sa.false()),
        sa.Column("parsed_uri", sa.Text()),
        sa.Column("worker_id", sa.Text()),
        sa.Column("lease_until", sa.DateTime(timezone=True)),
        sa.Column("duration_ms", sa.Integer()),
        sa.Column("peak_rss_mb", sa.Integer()),
        sa.Column("error_code", sa.Text()),
        sa.Column("error_detail", sa.Text()),
    )

    op.create_table(
        "chunks",
        sa.Column("id", pg.UUID(as_uuid=True), primary_key=True),
        sa.Column("doc_id", pg.UUID(as_uuid=True), sa.ForeignKey("documents.id", ondelete="CASCADE"), nullable=False),
        sa.Column("chunk_hash", sa.String(64), nullable=False),
        sa.Column("seq", sa.Integer(), nullable=False),
        sa.Column("text", sa.Text(), nullable=False),
        sa.Column("token_count", sa.Integer(), server_default="0"),
        sa.Column("page_start", sa.Integer()),
        sa.Column("page_end", sa.Integer()),
        sa.Column("heading_path", pg.ARRAY(sa.Text())),
        sa.Column("embedded_at", sa.DateTime(timezone=True)),
        sa.UniqueConstraint("doc_id", "chunk_hash", name="uq_chunk_hash"),
    )

    op.create_table(
        "events",
        sa.Column("id", sa.BigInteger(), primary_key=True, autoincrement=True),
        sa.Column("doc_id", pg.UUID(as_uuid=True)),
        sa.Column("shard_idx", sa.Integer()),
        sa.Column("level", sa.String(8), nullable=False),
        sa.Column("stage", sa.String(16)),
        sa.Column("code", sa.Text()),
        sa.Column("message", sa.Text(), nullable=False),
        sa.Column("context", pg.JSONB(), server_default="{}"),
        sa.Column("worker_id", sa.Text()),
        sa.Column("created_at", sa.DateTime(timezone=True), server_default=sa.func.now()),
    )
    op.create_index("ix_events_doc_created", "events", ["doc_id", "created_at"])
    op.create_index("ix_events_level_created", "events", ["level", "created_at"])

    op.create_table(
        "api_keys",
        sa.Column("id", pg.UUID(as_uuid=True), primary_key=True),
        sa.Column("name", sa.Text()),
        sa.Column("key_hash", sa.Text(), nullable=False),
        sa.Column("scopes", pg.ARRAY(sa.Text())),
        sa.Column("collections", pg.ARRAY(sa.Text())),
        sa.Column("last_used_at", sa.DateTime(timezone=True)),
        sa.Column("revoked_at", sa.DateTime(timezone=True)),
    )

    # metrics_rollup (PRD §10.1) — janitor writes one row per minute.
    op.create_table(
        "metrics_rollup",
        sa.Column("bucket", sa.DateTime(timezone=True), primary_key=True),
        sa.Column("pages_parsed", sa.Integer()),
        sa.Column("shards_done", sa.Integer()),
        sa.Column("shards_failed", sa.Integer()),
        sa.Column("chunks_embedded", sa.Integer()),
        sa.Column("parse_p50_ms", sa.Integer()),
        sa.Column("parse_p95_ms", sa.Integer()),
        sa.Column("peak_rss_p95_mb", sa.Integer()),
        sa.Column("queue_depth", pg.JSONB()),
        sa.Column("search_p95_ms", sa.Integer()),
        sa.Column("search_count", sa.Integer()),
    )


def downgrade() -> None:
    op.drop_table("metrics_rollup")
    op.drop_table("api_keys")
    op.drop_index("ix_events_level_created", table_name="events")
    op.drop_index("ix_events_doc_created", table_name="events")
    op.drop_table("events")
    op.drop_table("chunks")
    op.drop_table("shards")
    op.drop_index("ix_documents_state", table_name="documents")
    op.drop_table("documents")
    op.drop_table("collections")
    pg.ENUM(name="shard_state").drop(op.get_bind(), checkfirst=True)
    pg.ENUM(name="doc_state").drop(op.get_bind(), checkfirst=True)
