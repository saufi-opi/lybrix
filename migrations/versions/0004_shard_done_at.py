"""shards.done_at — per-shard completion timestamp (metrics rollup).

The janitor's metrics_rollup writer (PRD §10.1) needs per-minute windows
of settled shards: p50/p95 parse durations and pages_parsed per bucket
are windowed over shards.done_at, which no shard column carried.

Revision ID: 0004_shard_done_at
Revises: 0003_api_key_expiry_usage
Create Date: 2026-09-21
"""

from __future__ import annotations

import sqlalchemy as sa
from alembic import op

revision: str = "0004_shard_done_at"
down_revision: str = "0003_api_key_expiry_usage"
branch_labels: str | None = None
depends_on: str | None = None


def upgrade() -> None:
    op.add_column("shards", sa.Column("done_at", sa.DateTime(timezone=True), nullable=True))
    op.create_index("ix_shards_done_at", "shards", ["done_at"])


def downgrade() -> None:
    op.drop_index("ix_shards_done_at", table_name="shards")
    op.drop_column("shards", "done_at")
