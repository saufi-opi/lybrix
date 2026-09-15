"""parsed_uri: rewrite stale .json object keys to .md (key migration 411bcc7).

The parsed-shard store was migrated from mislabeled {idx}.json keys to {idx}.md
(server-side copy + delete, 2026-09-15). shards.parsed_uri captured the old key
at mark_shard_done() time; 15,995 rows still point at now-deleted objects.
This migration rewrites the extension in-place. Idempotent: .md rows untouched.

Revision ID: 0002_parsed_uri_md
Revises: 0001_initial
Create Date: 2026-09-15
"""

from __future__ import annotations

from alembic import op

revision: str = "0002_parsed_uri_md"
down_revision: str = "0001_initial"
branch_labels: str | None = None
depends_on: str | None = None


def upgrade() -> None:
    op.execute(
        """
        UPDATE shards
        SET parsed_uri = regexp_replace(parsed_uri, '\\.json$', '.md')
        WHERE parsed_uri LIKE '%.json'
        """
    )


def downgrade() -> None:
    # Reverting would recreate pointers to deleted objects — not meaningful.
    # Kept as a no-op with an explicit comment rather than a destructive rewrite.
    pass
