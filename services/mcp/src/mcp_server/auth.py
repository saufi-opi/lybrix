"""Bearer-key auth for the MCP server (PRD §7.2, §11).

The key's ``collections`` array scopes every tool call server-side:
passing a collection outside the key's scope is an error, not a filter.
Keys are stored as sha256 hashes (argon2 upgrade tracked in docs/adr).
"""

from __future__ import annotations

import hashlib
import uuid

from sqlalchemy import select
from sqlalchemy.orm import Session

from core.db.models import ApiKey

VALID_SCOPES = {"search", "ingest", "admin"}


class AuthError(Exception):
    pass


def authenticate(session: Session, authorization: str | None) -> ApiKey:
    if not authorization or not authorization.lower().startswith("bearer "):
        raise AuthError("missing bearer key")
    raw = authorization.split(" ", 1)[1].strip()
    key_hash = hashlib.sha256(raw.encode()).hexdigest()
    key = session.execute(
        select(ApiKey).where(ApiKey.key_hash == key_hash, ApiKey.revoked_at.is_(None))
    ).scalar_one_or_none()
    if key is None:
        raise AuthError("invalid key")
    if "search" not in (key.scopes or []):
        raise AuthError("key lacks search scope")
    return key


def assert_collection_allowed(key: ApiKey, collection_id: str | None) -> None:
    if collection_id is None:
        return
    allowed = key.collections or []
    if allowed and collection_id not in allowed:
        raise AuthError(f"key is not scoped to collection {collection_id!r}")


def hash_key(raw: str) -> str:
    return hashlib.sha256(raw.encode()).hexdigest()


def new_key_id() -> str:
    return str(uuid.uuid4())
