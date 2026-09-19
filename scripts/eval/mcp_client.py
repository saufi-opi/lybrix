"""Minimal MCP JSON-RPC client over stdlib urllib — no dependencies, no
imports from outside the repo.

Port of the verified Hermes pattern
(~/.hermes/skills/research/lybrix-operations/scripts/books-mcp-client.py),
reimplemented with urllib.request instead of curl-subprocess so the transport
is injectable and tests run with no network.

Design rules carried over from the reference client's live-verified behavior:
- Handshake: initialize -> capture mcp-session-id response header
  (case-insensitive) -> notifications/initialized (empty 202 body tolerated).
- tools/call sends the session header; a stale/lost session (-32600 "Missing
  session ID") re-handshakes once and retries.
- Response bodies arrive in three shapes: SSE (data: {...} lines), plain JSON,
  or empty — handle all three.
- result.content[0].text is itself a JSON string: parse twice, fall back to
  the raw string.

Config comes from env only: LYBRIX_MCP_URL and LYBRIX_MCP_TOKEN (Bearer).
Missing either raises McpConfigError naming the variable. A custom User-Agent
is always sent: Python-urllib/* is bot-blocked on the public CF-fronted URL
(the Tailscale endpoint is immune, but stay defensive).

"collection" disambiguation (critical): the MCP `search` tool's `collection`
argument is a payload filter on `collection_id` (services/mcp server.py ->
libs/retrieval search.py build_filter -> FieldCondition(key="collection_id")),
NOT the Qdrant collection name (hardcoded "chunks"). A wrong value does not
error — it silently filters to zero rows. Collections in the payload sense are
Postgres rows with id/name (server.py list_collections_impl); run.py resolves
names to ids before any search call.
"""

from __future__ import annotations

import json
import os
import re
import urllib.request
from collections.abc import Callable
from typing import Any

PROTOCOL_VERSION = "2024-11-05"
CLIENT_INFO = {"name": "lybrix-eval", "version": "0.1.0"}
USER_AGENT = "lybrix-eval/0.1.0"
# 60 s matches the reference client's curl --max-time; long query matrices
# occasionally hit slow paths on the server.
REQUEST_TIMEOUT_S = 60


class McpConfigError(RuntimeError):
    """Raised when LYBRIX_MCP_URL / LYBRIX_MCP_TOKEN is missing from env."""


class McpRpcError(RuntimeError):
    """Raised when the server returns a JSON-RPC error with no recovery path."""


# Transport: post_fn(url, headers, body_bytes) -> (status, response_headers,
# body_bytes). response_headers is a mapping-like object supporting
# case-insensitive get() (http.client.HTTPMessage or a plain dict both work).
PostFn = Callable[[str, dict[str, str], bytes], tuple[int, Any, bytes]]

# Matches "mcp-session-id" in response header blocks regardless of case;
# works on both real header maps (via .get) and raw header text (fakes).
_SESSION_HEADER_RE = re.compile(r"(?i)mcp-session-id:\s*(\S+)")


def _session_id_from(headers: Any) -> str | None:
    """Extract the session id from a response-header object, tolerating
    dict-likes without case-insensitive lookup."""
    try:
        value = headers.get("mcp-session-id")
        if value:
            return value.strip()
        value = headers.get("Mcp-Session-Id")
        if value:
            return value.strip()
    except AttributeError:
        pass
    # Fallback: regex over the raw header text (covers odd fake transports).
    match = _SESSION_HEADER_RE.search(str(headers))
    return match.group(1) if match else None


def default_post_fn(
    url: str, headers: dict[str, str], body_bytes: bytes
) -> tuple[int, Any, bytes]:
    """Stdlib urllib transport — the only place real network I/O happens."""
    request = urllib.request.Request(url, data=body_bytes, headers=headers, method="POST")
    try:
        with urllib.request.urlopen(request, timeout=REQUEST_TIMEOUT_S) as response:
            return response.status, response.headers, response.read()
    except urllib.error.HTTPError as exc:  # 4xx/5xx still carry headers+body
        return exc.code, exc.headers, exc.read()


class McpClient:
    """Self-contained MCP client; pass post_fn to run fully offline in tests."""

    def __init__(
        self,
        post_fn: PostFn | None = None,
        url: str | None = None,
        token: str | None = None,
    ):
        self._post_fn = post_fn or default_post_fn
        self.url = url if url is not None else os.environ.get("LYBRIX_MCP_URL", "")
        self.token = token if token is not None else os.environ.get("LYBRIX_MCP_TOKEN", "")
        if not self.url:
            raise McpConfigError("LYBRIX_MCP_URL is not set")
        if not self.token:
            raise McpConfigError("LYBRIX_MCP_TOKEN is not set")
        self.session_id: str | None = None

    # -- transport ---------------------------------------------------------

    def _rpc(self, method: str, params: dict[str, Any] | None) -> tuple[int, Any, bytes]:
        body: dict[str, Any] = {"jsonrpc": "2.0", "id": 1, "method": method}
        if params is not None:
            body["params"] = params
        headers = {
            "Content-Type": "application/json",
            "Accept": "application/json, text/event-stream",
            "Authorization": f"Bearer {self.token}",
            "User-Agent": USER_AGENT,
        }
        if self.session_id:
            headers["mcp-session-id"] = self.session_id
        status, response_headers, response_body = self._post_fn(
            self.url, headers, json.dumps(body).encode()
        )
        return status, response_headers, response_body

    @staticmethod
    def parse_body(raw: bytes) -> dict[str, Any] | None:
        """SSE (first data: line), plain JSON, or empty -> None."""
        text = raw.decode("utf-8", errors="replace")
        for line in text.splitlines():
            if line.startswith("data:"):
                return json.loads(line[len("data:"):].strip())
        if text.strip():
            return json.loads(text)
        return None

    @staticmethod
    def parse_result_text(text: str) -> Any:
        """result.content[0].text is itself a JSON string — parse twice,
        fall back to the raw string when it is not JSON."""
        try:
            return json.loads(text)
        except (ValueError, TypeError):
            return text

    def extract_result(self, payload: dict[str, Any] | None) -> Any:
        if payload is None:
            return None
        if payload.get("error"):
            raise McpRpcError(str(payload["error"]))
        content = payload.get("result", {}).get("content") or []
        if not content:
            return None
        return self.parse_result_text(content[0].get("text", ""))

    # -- handshake ---------------------------------------------------------

    def handshake(self) -> None:
        """initialize -> capture session id -> notifications/initialized."""
        status, headers, body = self._rpc(
            "initialize",
            {
                "protocolVersion": PROTOCOL_VERSION,
                "capabilities": {},
                "clientInfo": CLIENT_INFO,
            },
        )
        payload = self.parse_body(body)
        if payload is not None and payload.get("error"):
            raise McpRpcError(f"initialize failed: {payload['error']}")
        self.session_id = _session_id_from(headers)
        # notifications/initialized: server replies HTTP 202 with no body.
        self._rpc("notifications/initialized", {})

    # -- tool calls --------------------------------------------------------

    def call(self, name: str, arguments: dict[str, Any], retry: bool = True) -> Any:
        """tools/call with the session header; a stale/lost session
        (-32600 "Missing session ID") re-handshakes once and retries."""
        _status, _headers, body = self._rpc("tools/call", {"name": name, "arguments": arguments})
        try:
            payload = self.parse_body(body)
        except ValueError:
            payload = None
        if payload is None or payload.get("error"):
            error = payload.get("error") if payload else None
            if retry and error and "missing session" in str(error).lower():
                self.handshake()
                return self.call(name, arguments, retry=False)
            if payload is None:
                if retry:  # unparseable/empty body mid-run: retry via fresh handshake
                    self.handshake()
                    return self.call(name, arguments, retry=False)
                raise McpRpcError(f"tools/call {name}: unparseable response")
            raise McpRpcError(f"tools/call {name} failed: {error}")
        return self.extract_result(payload)

    # -- tool conveniences -------------------------------------------------

    def search(self, query: str, collection: str | None = None, top_k: int = 8) -> list[dict[str, Any]]:
        """search returns items with doc_title, page_start/page_end (sometimes
        null), heading_path (list), text, score, doc_id, chunk_id."""
        arguments: dict[str, Any] = {"query": query, "top_k": top_k}
        if collection is not None:
            arguments["collection"] = collection
        result = self.call("search", arguments)
        return result if isinstance(result, list) else []

    def list_documents(
        self,
        collection: str | None = None,
        limit: int = 50,
        state: str | None = None,
        query: str | None = None,
    ) -> list[dict[str, Any]]:
        arguments: dict[str, Any] = {"limit": limit}
        if collection is not None:
            arguments["collection"] = collection
        if state is not None:
            arguments["state"] = state
        if query is not None:
            arguments["query"] = query
        result = self.call("list_documents", arguments)
        return result if isinstance(result, list) else []

    def list_collections(self) -> list[dict[str, Any]]:
        """Returns [{id, name, embedding_model, doc_count}] — Postgres rows
        whose `id` is what the search tool's collection arg actually filters on."""
        result = self.call("list_collections", {})
        return result if isinstance(result, list) else []
