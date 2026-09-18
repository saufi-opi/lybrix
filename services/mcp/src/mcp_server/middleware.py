"""Per-request bearer-key gate for the MCP HTTP transport (PRD §7.2, §11).

Two layers, both needed:

* ``McpAuthMiddleware`` is pure ASGI middleware, wired via
  ``mcp.http_app(middleware=[...])`` in ``main()``. It must live at the ASGI
  layer because a FastMCP-level ``Middleware`` can only fail a request with a
  JSON-RPC error frame on HTTP 200 — there is no way to emit a real HTTP 401
  from inside the message pipeline. /health stays open (compose healthchecks,
  load balancers).
* ``UsageMiddleware`` is a FastMCP ``Middleware`` that records one ``key_usage``
  row per MCP method (search, tools/list, ...) — the ASGI layer runs before
  routing, so the method name is only known here.

The authenticated ``ApiKey`` travels to tools through ``current_api_key()``
(a ContextVar set by the ASGI gate, reset in ``finally`` so a keep-alive
worker never carries a stale key into a later request). Everything
DB-touching is best-effort: an analytics write must never fail a search.
"""

from __future__ import annotations

import contextvars
import logging
from datetime import UTC, datetime

from core.db.models import ApiKey, KeyUsage
from fastmcp.server.middleware import Middleware
from starlette.datastructures import Headers
from starlette.responses import JSONResponse
from starlette.types import ASGIApp, Receive, Scope, Send

from mcp_server.auth import AuthError, authenticate

logger = logging.getLogger(__name__)

_api_key: contextvars.ContextVar[ApiKey | None] = contextvars.ContextVar(
    "mcp_api_key", default=None
)


def current_api_key() -> ApiKey | None:
    """The ApiKey authenticated for the in-flight request (None pre-auth)."""
    return _api_key.get()


class McpAuthMiddleware:
    """ASGI gate: every path except /health requires a valid bearer key.

    On success the ApiKey is stored in the ContextVar for the rest of the
    request, last_used_at is bumped and a key_usage row written — both
    best-effort inside their own short session.
    """

    def __init__(self, app: ASGIApp, session_factory) -> None:
        self.app = app
        self._session_factory = session_factory  # bound in main()

    async def __call__(self, scope: Scope, receive: Receive, send: Send) -> None:
        if scope["type"] != "http" or scope.get("path") == "/health":
            await self.app(scope, receive, send)
            return
        authorization = Headers(scope=scope).get("authorization")
        try:
            with self._session_factory() as session:
                key = authenticate(session, authorization)
                self._touch(session, key)
        except AuthError as exc:
            response = JSONResponse(
                {"error": "unauthorized", "detail": str(exc)},
                status_code=401,
                headers={"WWW-Authenticate": "Bearer"},
            )
            await response(scope, receive, send)
            return
        token = _api_key.set(key)
        try:
            await self.app(scope, receive, send)
        finally:
            _api_key.reset(token)

    def _touch(self, session, key) -> None:
        """last_used_at bump + one gate-level key_usage row; best-effort."""
        try:
            key.last_used_at = datetime.now(UTC)
            session.add(key)
            session.add(KeyUsage(api_key_id=key.id, surface="mcp", action="http"))
            session.commit()
        except Exception:
            logger.warning("key usage bookkeeping failed", exc_info=True)
            session.rollback()


class UsageMiddleware(Middleware):
    """Record one key_usage row per authenticated MCP method call.

    Runs inside the message pipeline (after the ASGI gate has already set
    the ContextVar), so ``current_api_key()`` is populated here.
    """

    def __init__(self, session_factory) -> None:
        self._session_factory = session_factory

    async def on_request(self, context, call_next):
        key = current_api_key()
        if key is not None:
            action = context.method or "unknown"
            if action == "tools/call" and context.message is not None:
                name = getattr(context.message, "name", None)
                if name:
                    action = f"tools/call:{name}"
            try:
                with self._session_factory() as session:
                    session.add(
                        KeyUsage(api_key_id=key.id, surface="mcp", action=action)
                    )
                    session.commit()
            except Exception:
                logger.warning("key_usage write failed", exc_info=True)
        return await call_next(context)
