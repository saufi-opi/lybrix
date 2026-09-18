"""MCP auth gate (PLAN P0): the real build_server() app behind the ASGI
auth middleware, driven over streamable HTTP with fakes.

The fake ``authenticate`` consults a ``{raw_key: key_row}`` dict and raises
``AuthError`` exactly like the real one (missing bearer / invalid / revoked /
expired / lacks-scope). ``FakeSession.execute`` cannot evaluate real
SQLAlchemy statements, so the statement path is not exercised here; the real
``authenticate()`` logic — including the new expiry branch — is covered in
``test_authenticate_expiry_unit`` with a session whose ``execute()`` returns
``SimpleNamespace(scalar_one_or_none=lambda: row)``.
"""

from __future__ import annotations

import contextlib
import hashlib
import json
import uuid
from datetime import UTC, datetime, timedelta
from types import SimpleNamespace
from unittest.mock import patch

import mcp_server.middleware as middleware_module
import mcp_server.server as server_module
import pytest
from core.db.models import KeyUsage
from mcp_server.auth import AuthError, authenticate
from starlette.middleware import Middleware as ASGIMiddleware
from starlette.testclient import TestClient

from tests.conftest import make_settings

RAW_KEY = "ragk_test_good"
KEY_ID = "00000000-0000-0000-0000-000000000001"

INITIALIZE = {
    "jsonrpc": "2.0",
    "id": 1,
    "method": "initialize",
    "params": {
        "protocolVersion": "2024-11-05",
        "capabilities": {},
        "clientInfo": {"name": "t", "version": "0"},
    },
}
ACCEPT = {"Accept": "application/json, text/event-stream"}


class FakeKey:
    """Duck-typed ApiKey row: only the attributes auth code touches."""

    def __init__(self, scopes=("search",), collections=("col-a",), revoked_at=None, expires_at=None):
        self.id = uuid.UUID(KEY_ID)
        self.name = "test key"
        self.scopes = list(scopes)
        self.collections = list(collections) if collections is not None else None
        self.revoked_at = revoked_at
        self.expires_at = expires_at
        self.last_used_at = None


class RecordingSession:
    """Records add()ed objects; never touches a database."""

    def __init__(self):
        self.added = []
        self.commits = 0

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False

    def add(self, obj):
        self.added.append(obj)

    def commit(self):
        self.commits += 1

    def rollback(self):
        pass


class FakeFactory:
    def __init__(self):
        self.sessions = []

    def __call__(self):
        session = RecordingSession()
        self.sessions.append(session)
        return session


def _fake_auth(keys: dict[str, FakeKey]):
    """Same contract as mcp_server.auth.authenticate, minus the SQL."""

    def fake_auth(session, authorization):
        if not authorization or not authorization.lower().startswith("bearer "):
            raise AuthError("missing bearer key")
        raw = authorization.split(" ", 1)[1].strip()
        key = keys.get(raw)
        now = datetime.now(UTC)
        if key is None or key.revoked_at is not None:
            raise AuthError("invalid key")
        if key.expires_at is not None and key.expires_at < now:
            raise AuthError("key expired")
        if "search" not in (key.scopes or []):
            raise AuthError("key lacks search scope")
        return key

    return fake_auth


@contextlib.contextmanager
def _app(keys: dict[str, FakeKey]):
    """The real build_server() app with the ASGI gate, wired exactly as
    main() does (ASGIMiddleware wrapper inside mcp.http_app)."""
    factory = FakeFactory()
    with (
        patch.object(middleware_module, "authenticate", _fake_auth(keys)),
        patch.object(server_module, "make_engine", return_value=None),
        patch.object(server_module, "make_session_factory", return_value=factory),
    ):
        mcp = server_module.build_server(make_settings())
        app = mcp.http_app(
            path="/mcp",
            middleware=[
                ASGIMiddleware(
                    middleware_module.McpAuthMiddleware, session_factory=factory
                )
            ],
        )
        with TestClient(app) as client:
            yield client, factory


def _data_frames(text: str) -> list[dict]:
    return [json.loads(line[6:]) for line in text.splitlines() if line.startswith("data: ")]


def _open_session(client, headers):
    """initialize + notifications/initialized; returns headers with the
    mcp-session-id the server assigned."""
    r = client.post("/mcp", json=INITIALIZE, headers=headers)
    assert r.status_code == 200
    headers = {**headers, "mcp-session-id": r.headers["mcp-session-id"]}
    client.post(
        "/mcp", json={"jsonrpc": "2.0", "method": "notifications/initialized"}, headers=headers
    )
    return headers


def _usage_rows(factory) -> list[KeyUsage]:
    return [o for s in factory.sessions for o in s.added if isinstance(o, KeyUsage)]


def test_keyless_request_is_401():
    with _app({RAW_KEY: FakeKey()}) as (client, _factory):
        r = client.post("/mcp", json=INITIALIZE, headers=ACCEPT)
    assert r.status_code == 401  # real HTTP status, not a JSON-RPC frame
    assert r.headers.get("www-authenticate") == "Bearer"
    assert r.json()["error"] == "unauthorized"


def test_bad_key_is_401():
    with _app({RAW_KEY: FakeKey()}) as (client, _factory):
        r = client.post("/mcp", json=INITIALIZE, headers={**ACCEPT, "Authorization": "Bearer deadbeef"})
    assert r.status_code == 401
    assert "invalid key" in r.json()["detail"]


def test_revoked_key_is_401():
    keys = {RAW_KEY: FakeKey(revoked_at=datetime.now(UTC))}
    with _app(keys) as (client, _factory):
        r = client.post("/mcp", json=INITIALIZE, headers={**ACCEPT, "Authorization": f"Bearer {RAW_KEY}"})
    assert r.status_code == 401


def test_expired_key_is_401():
    keys = {RAW_KEY: FakeKey(expires_at=datetime.now(UTC) - timedelta(days=1))}
    with _app(keys) as (client, _factory):
        r = client.post("/mcp", json=INITIALIZE, headers={**ACCEPT, "Authorization": f"Bearer {RAW_KEY}"})
    assert r.status_code == 401
    assert "expired" in r.json()["detail"]


def test_valid_key_gets_200_and_last_used_at_set():
    key = FakeKey()
    with _app({RAW_KEY: key}) as (client, factory):
        r = client.post("/mcp", json=INITIALIZE, headers={**ACCEPT, "Authorization": f"Bearer {RAW_KEY}"})
    assert r.status_code == 200
    assert key.last_used_at is not None
    gate_rows = [u for u in _usage_rows(factory) if u.surface == "mcp" and u.action == "http"]
    assert len(gate_rows) == 1
    assert gate_rows[0].api_key_id == key.id


def test_health_open_without_key():
    with _app({RAW_KEY: FakeKey()}) as (client, _factory):
        r = client.get("/health")
    assert r.status_code == 200


def test_disallowed_collection_is_error():
    with _app({RAW_KEY: FakeKey(collections=("col-a",))}) as (client, factory):
        headers = _open_session(client, {**ACCEPT, "Authorization": f"Bearer {RAW_KEY}"})
        call = {
            "jsonrpc": "2.0",
            "id": 2,
            "method": "tools/call",
            "params": {"name": "search", "arguments": {"query": "q", "collection": "col-b"}},
        }
        r = client.post("/mcp", json=call, headers=headers)
    assert r.status_code == 200
    frame = _data_frames(r.text)[0]
    assert frame["result"]["isError"] is True
    assert "not scoped" in frame["result"]["content"][0]["text"]


def test_allowed_collection_returns_results():
    with _app({RAW_KEY: FakeKey(collections=("col-a",))}) as (client, factory):
        headers = _open_session(client, {**ACCEPT, "Authorization": f"Bearer {RAW_KEY}"})
        call = {
            "jsonrpc": "2.0",
            "id": 3,
            "method": "tools/call",
            "params": {"name": "search", "arguments": {"query": "q", "collection": "col-a"}},
        }
        canned = [{"doc_title": "T", "page_start": 1, "page_end": 2, "text": "hi"}]
        with patch.object(server_module, "search_impl", lambda *a, **k: canned):
            r = client.post("/mcp", json=call, headers=headers)
    assert r.status_code == 200
    frame = _data_frames(r.text)[0]
    assert frame["result"]["isError"] is False
    assert "doc_title" in r.text
    # UsageMiddleware recorded the method-level row alongside the gate row.
    actions = [u.action for u in _usage_rows(factory)]
    assert "tools/call:search" in actions


class _RowSession:
    """Fake session for the real authenticate(): execute() yields the row."""

    def __init__(self, row):
        self._row = row

    def execute(self, stmt, *a, **kw):
        return SimpleNamespace(scalar_one_or_none=lambda: self._row)


def _row(revoked_at=None, expires_at=None, scopes=("search",)):
    return SimpleNamespace(
        key_hash=hashlib.sha256(b"ragk_unit").hexdigest(),
        revoked_at=revoked_at,
        expires_at=expires_at,
        scopes=list(scopes),
        collections=None,
    )


def test_authenticate_expiry_unit():
    auth = "Bearer ragk_unit"
    expired = _row(expires_at=datetime.now(UTC) - timedelta(seconds=1))
    with pytest.raises(AuthError, match="key expired"):
        authenticate(_RowSession(expired), auth)
    revoked = _row(revoked_at=datetime.now(UTC))
    with pytest.raises(AuthError, match="invalid key"):
        authenticate(_RowSession(revoked), auth)
    valid = _row()
    assert authenticate(_RowSession(valid), auth) is valid
