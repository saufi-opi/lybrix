"""Shared FastAPI dependencies: DB session per request and bearer-key auth
with scope enforcement (PRD §11)."""

from __future__ import annotations

import uuid
from collections.abc import Generator

from fastapi import Depends, Header, HTTPException, status
from sqlalchemy.orm import Session

from core.db.models import ApiKey
from core.db.session import make_engine, make_session_factory

_engine = None
_factory = None


def get_session() -> Generator[Session, None, None]:
    global _engine, _factory
    if _factory is None:
        _engine = make_engine()
        _factory = make_session_factory(_engine)
    session = _factory()
    try:
        yield session
        session.commit()
    except Exception:
        session.rollback()
        raise
    finally:
        session.close()


VALID_SCOPES = {"search", "ingest", "admin"}


def require_scope(scope: str):
    """Dependency factory: 401 without a valid key, 403 without the scope."""

    def _dep(
        authorization: str | None = Header(default=None),
        session: Session = Depends(get_session),
    ) -> ApiKey:
        if not authorization or not authorization.lower().startswith("bearer "):
            raise HTTPException(status.HTTP_401_UNAUTHORIZED, "missing bearer key")
        raw = authorization.split(" ", 1)[1].strip()
        # key_hash is stored as sha256(raw) — argon2 upgrade tracked in docs.
        import hashlib

        key_hash = hashlib.sha256(raw.encode()).hexdigest()
        key = session.query(ApiKey).filter(ApiKey.key_hash == key_hash).first()
        if key is None or key.revoked_at is not None:
            raise HTTPException(status.HTTP_401_UNAUTHORIZED, "invalid key")
        scopes = set(key.scopes or [])
        if scope not in scopes:
            raise HTTPException(
                status.HTTP_403_FORBIDDEN, f"key lacks required scope: {scope}"
            )
        return key

    return _dep
