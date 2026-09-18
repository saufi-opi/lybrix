"""Keys router (PLAN P1): create/list/revoke against a fake session, plus
the shared deps helpers (_reject_if_unusable, _record_usage).

Endpoint functions are called directly with fakes — the pattern of
tests/test_system_queues.py. No TestClient, no real DB.
"""

from __future__ import annotations

import hashlib
import uuid
from datetime import UTC, datetime, timedelta
from types import SimpleNamespace

import pytest
from api.deps import _record_usage, _reject_if_unusable
from api.routers.keys import KeyCreate, _out, create_key, list_keys, revoke_key
from core.db.models import ApiKey, Event, KeyUsage
from fastapi import HTTPException


class FakeKeysSession:
    """Records add()ed objects; supports get/execute/flush used by the
    router. execute() only understands select(ApiKey)."""

    def __init__(self, keys=()):
        self.added = []
        self._keys = list(keys)
        self.flushed = 0

    def add(self, obj):
        self.added.append(obj)

    def flush(self):
        self.flushed += 1
        for obj in self.added:
            if isinstance(obj, ApiKey) and obj.id is None:
                obj.id = uuid.uuid4()

    def get(self, model, key):
        for k in self._keys + [o for o in self.added if isinstance(o, ApiKey)]:
            if k.id == key:
                return k
        return None

    def execute(self, stmt, *a, **kw):
        return SimpleNamespace(scalars=lambda: SimpleNamespace(all=lambda: list(self._keys)))


def _admin_key(scopes=("admin",)):
    return ApiKey(
        id=uuid.uuid4(),
        name="admin",
        key_hash="h" * 64,
        scopes=list(scopes),
    )


def _raw_of(row) -> str | None:
    return next(
        (v for v in vars(row).values() if isinstance(v, str) and v.startswith("ragk_")), None
    )


def test_create_returns_raw_key_once_and_stores_hash():
    session = FakeKeysSession()
    out = create_key(
        KeyCreate(name="ci key", scopes=["search"], expires_in="never"),
        _admin_key(),
        session,
    )
    # raw key looks right and is present exactly once in the response
    assert out.raw_key.startswith("ragk_")
    assert len(out.raw_key) > 40
    row = next(o for o in session.added if isinstance(o, ApiKey))
    assert row.key_hash == hashlib.sha256(out.raw_key.encode()).hexdigest()
    # no plaintext raw key anywhere on the row
    assert not hasattr(row, "raw_key")
    assert _raw_of(row) is None
    # exactly one audit event, and it carries no key material
    events = [o for o in session.added if isinstance(o, Event)]
    assert len(events) == 1
    assert events[0].stage == "keys"
    assert events[0].code == "created"
    ctx = events[0].context
    assert set(ctx) == {"key_id", "name", "scopes"}
    assert "ragk_" not in str(ctx)
    assert out.raw_key not in str(ctx)


def test_create_validates_scopes():
    with pytest.raises(HTTPException) as exc:
        create_key(
            KeyCreate(name="bad", scopes=["banana"], expires_in="never"),
            _admin_key(),
            FakeKeysSession(),
        )
    assert exc.value.status_code == 422


@pytest.mark.parametrize("choice,days", [("1d", 1), ("7d", 7), ("30d", 30), ("90d", 90)])
def test_expiry_choices(choice, days):
    session = FakeKeysSession()
    before = datetime.now(UTC)
    out = create_key(
        KeyCreate(name="k", scopes=["search"], expires_in=choice),
        _admin_key(),
        session,
    )
    row = next(o for o in session.added if isinstance(o, ApiKey))
    expected = before + timedelta(days=days)
    assert abs((row.expires_at - expected).total_seconds()) < 5
    assert abs((out.expires_at - expected).total_seconds()) < 5


def test_expiry_never_is_none():
    session = FakeKeysSession()
    out = create_key(
        KeyCreate(name="k", scopes=["search"], expires_in="never"),
        _admin_key(),
        session,
    )
    assert out.expires_at is None
    row = next(o for o in session.added if isinstance(o, ApiKey))
    assert row.expires_at is None


def test_list_never_exposes_hash_or_raw():
    stored = ApiKey(
        id=uuid.uuid4(),
        name="old",
        key_hash="a" * 64,
        scopes=["search"],
        last_used_at=None,
    )
    session = FakeKeysSession(keys=[stored])
    rows = list_keys(_admin_key(), session)
    assert len(rows) == 1
    dumped = rows[0].model_dump()
    assert set(dumped) == {
        "id",
        "name",
        "scopes",
        "collections",
        "last_used_at",
        "expires_at",
        "revoked_at",
    }
    assert "key_hash" not in str(dumped)
    assert "a" * 64 not in str(dumped)


def test_revoke_sets_revoked_at_and_event():
    target = ApiKey(
        id=uuid.uuid4(),
        name="victim",
        key_hash="b" * 64,
        scopes=["search"],
    )
    session = FakeKeysSession(keys=[target])
    out = revoke_key(str(target.id), _admin_key(), session)
    assert out.revoked_at is not None
    assert target.revoked_at is not None
    events = [o for o in session.added if isinstance(o, Event)]
    assert len(events) == 1
    assert events[0].code == "revoked"
    # idempotent: revoking again is a no-op — no second event
    revoke_key(str(target.id), _admin_key(), session)
    assert len([o for o in session.added if isinstance(o, Event)]) == 1


def test_revoke_unknown_id_is_404():
    with pytest.raises(HTTPException) as exc:
        revoke_key(str(uuid.uuid4()), _admin_key(), FakeKeysSession())
    assert exc.value.status_code == 404
    with pytest.raises(HTTPException) as exc:
        revoke_key("not-a-uuid", _admin_key(), FakeKeysSession())
    assert exc.value.status_code == 404


def test_usage_rows_written():
    key = ApiKey(id=uuid.uuid4(), name="k", key_hash="c" * 64, scopes=["search"])
    session = FakeKeysSession()
    _record_usage(session, key, action="POST /v1/keys")
    rows = [o for o in session.added if isinstance(o, KeyUsage)]
    assert len(rows) == 1
    assert rows[0].surface == "api"
    assert rows[0].action == "POST /v1/keys"
    assert rows[0].api_key_id == key.id
    assert key.last_used_at is not None


def test_reject_if_unusable_expired_and_revoked():
    key = ApiKey(id=uuid.uuid4(), name="k", key_hash="d" * 64, scopes=["search"])
    key.revoked_at = datetime.now(UTC)
    with pytest.raises(HTTPException) as exc:
        _reject_if_unusable(key)
    assert exc.value.status_code == 401
    key.revoked_at = None
    key.expires_at = datetime.now(UTC) - timedelta(seconds=1)
    with pytest.raises(HTTPException) as exc:
        _reject_if_unusable(key)
    assert exc.value.status_code == 401
    assert exc.value.detail == "key expired"
    key.expires_at = None
    _reject_if_unusable(key)  # valid: no raise


def test_out_roundtrip_shape():
    key = ApiKey(
        id=uuid.uuid4(),
        name="n",
        key_hash="e" * 64,
        scopes=["search"],
        expires_at=None,
    )
    assert _out(key).model_dump()["id"] == str(key.id)


def test_deps_module_still_hash_importable():
    # backward compat: mcp_server.auth re-exports hash_key from core.keys
    import mcp_server.auth as mcp_auth

    assert mcp_auth.hash_key("x") == hashlib.sha256(b"x").hexdigest()
