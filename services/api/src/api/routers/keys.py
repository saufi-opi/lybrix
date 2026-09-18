"""API key management (P1): create, list, revoke — admin scope only.

Raw keys are shown exactly once, in the create response. Only the sha256
hash is stored; raw keys never appear in logs, events, or list responses.
"""

from __future__ import annotations

import secrets
import uuid
from datetime import UTC, datetime, timedelta
from typing import Literal

from core.db.models import ApiKey
from core.events import write_event
from core.keys import hash_key
from fastapi import APIRouter, Depends, HTTPException
from pydantic import BaseModel, Field
from sqlalchemy import select
from sqlalchemy.orm import Session

from api.deps import get_session, require_scope

router = APIRouter(prefix="/v1/keys", tags=["keys"])

VALID_SCOPES = {"search", "ingest", "admin"}
ExpiresIn = Literal["1d", "7d", "30d", "90d", "never"]
_EXPIRY_DAYS = {"1d": 1, "7d": 7, "30d": 30, "90d": 90}


class KeyCreate(BaseModel):
    name: str = Field(min_length=1, max_length=200)
    scopes: list[str] = Field(min_length=1)
    collections: list[str] | None = None
    expires_in: ExpiresIn = "never"


class KeyOut(BaseModel):
    id: str
    name: str | None
    scopes: list[str] | None
    collections: list[str] | None
    last_used_at: datetime | None
    expires_at: datetime | None
    revoked_at: datetime | None


class KeyCreated(KeyOut):
    raw_key: str


def _out(key: ApiKey) -> KeyOut:
    return KeyOut(
        id=str(key.id),
        name=key.name,
        scopes=key.scopes,
        collections=key.collections,
        last_used_at=key.last_used_at,
        expires_at=key.expires_at,
        revoked_at=key.revoked_at,
    )


@router.post("", status_code=201, response_model=KeyCreated)
def create_key(
    body: KeyCreate,
    key=Depends(require_scope("admin")),
    session: Session = Depends(get_session),
):
    invalid = sorted(set(body.scopes) - VALID_SCOPES)
    if invalid:
        raise HTTPException(status_code=422, detail=f"unknown scopes: {invalid}")
    raw_key = "ragk_" + secrets.token_urlsafe(32)
    expires_at = (
        None
        if body.expires_in == "never"
        else datetime.now(UTC) + timedelta(days=_EXPIRY_DAYS[body.expires_in])
    )
    row = ApiKey(
        name=body.name,
        key_hash=hash_key(raw_key),
        scopes=body.scopes,
        collections=body.collections,
        expires_at=expires_at,
    )
    session.add(row)
    session.flush()  # assign row.id before the audit event
    write_event(
        session,
        "info",
        "keys",
        f"api key {body.name!r} created (scopes: {', '.join(body.scopes)})",
        code="created",
        context={"key_id": str(row.id), "name": body.name, "scopes": body.scopes},
    )
    return KeyCreated(
        **_out(row).model_dump(),
        raw_key=raw_key,
    )


@router.get("", response_model=list[KeyOut])
def list_keys(
    key=Depends(require_scope("admin")),
    session: Session = Depends(get_session),
):
    rows = session.execute(select(ApiKey).order_by(ApiKey.last_used_at.desc().nullslast())).scalars().all()
    return [_out(k) for k in rows]


@router.post("/{key_id}/revoke", response_model=KeyOut)
def revoke_key(
    key_id: str,
    key=Depends(require_scope("admin")),
    session: Session = Depends(get_session),
):
    try:
        target_id = uuid.UUID(key_id)
    except ValueError as exc:
        raise HTTPException(status_code=404, detail="key not found") from exc
    target = session.get(ApiKey, target_id)
    if target is None:
        raise HTTPException(status_code=404, detail="key not found")
    if target.revoked_at is None:
        target.revoked_at = datetime.now(UTC)
        write_event(
            session,
            "info",
            "keys",
            f"api key {target.name!r} revoked",
            code="revoked",
            context={"key_id": str(target.id), "name": target.name},
        )
    return _out(target)
