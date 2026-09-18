"""Bearer-key auth for the MCP server (PRD §7.2, §11).

The key's ``collections`` array scopes every tool call server-side:
passing a collection outside the key's scope is an error, not a filter.
Keys are stored as sha256 hashes (argon2 upgrade tracked in docs/adr).
A key is usable only while it is neither revoked nor expired: the
validity rule lives in ``authenticate()`` (mcp) and ``deps.require_scope``
(api) — revoked_at set, or expires_at in the past, both mean 401.
"""

from __future__ import annotations

import uuid
from datetime import UTC, datetime

from core.db.models import ApiKey
from core.keys import hash_key  # re-exported: existing imports keep working
from sqlalchemy import select
from sqlalchemy.orm import Session

VALID_SCOPES = {"search", "ingest", "admin"}


class AuthError(Exception):
    pass


def authenticate(session: Session, authorization: str | None) -> ApiKey:
    if not authorization or not authorization.lower().startswith("bearer "):
        raise AuthError("missing bearer key")
    raw = authorization.split(" ", 1)[1].strip()
    key_hash = hash_key(raw)
    key = session.execute(
        select(ApiKey).where(ApiKey.key_hash == key_hash)
    ).scalar_one_or_none()
    now = datetime.now(UTC)
    if key is None or key.revoked_at is not None:
        raise AuthError("invalid key")
    if key.expires_at is not None and key.expires_at < now:
        raise AuthError("key expired")
    if "search" not in (key.scopes or []):
        raise AuthError("key lacks search scope")
    return key


def assert_collection_allowed(key: ApiKey, collection_id: str | None) -> None:
    if collection_id is None:
        return
    allowed = key.collections or []
    if allowed and collection_id not in allowed:
        raise AuthError(f"key is not scoped to collection {collection_id!r}")


def new_key_id() -> str:
    return str(uuid.uuid4())
