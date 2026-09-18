"""api_keys.expires_at + key_usage table (P1 key management).

- expires_at: optional TTL enforced by both the API require_scope and the
  MCP authenticate().
- key_usage: one row per authenticated request per surface (mcp|api), for
  the admin dashboard's per-key activity view.

Revision ID: 0003_api_key_expiry_usage
Revises: 0002_parsed_uri_md
Create Date: 2026-09-18
"""

from __future__ import annotations

import sqlalchemy as sa
from alembic import op
from sqlalchemy.dialects import postgresql as pg

revision: str = "0003_api_key_expiry_usage"
down_revision: str = "0002_parsed_uri_md"
branch_labels: str | None = None
depends_on: str | None = None


def upgrade() -> None:
    op.add_column(
        "api_keys",
        sa.Column("expires_at", sa.DateTime(timezone=True), nullable=True),
    )
    op.create_table(
        "key_usage",
        sa.Column("id", sa.BigInteger(), primary_key=True, autoincrement=True),
        sa.Column(
            "api_key_id",
            pg.UUID(as_uuid=True),
            sa.ForeignKey("api_keys.id"),
            nullable=False,
        ),
        sa.Column("surface", sa.Text(), nullable=False),
        sa.Column("action", sa.Text(), nullable=False),
        sa.Column("created_at", sa.DateTime(timezone=True), server_default=sa.func.now()),
    )
    op.create_index(
        "ix_key_usage_key_created", "key_usage", ["api_key_id", "created_at"]
    )


def downgrade() -> None:
    op.drop_index("ix_key_usage_key_created", table_name="key_usage")
    op.drop_table("key_usage")
    op.drop_column("api_keys", "expires_at")
