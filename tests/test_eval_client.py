"""Tests for scripts/eval/mcp_client.py — fake transport, NO network.

Every test drives McpClient through an injected post_fn; the fake transport
records requests and replays canned responses so handshake, session capture,
body parsing, and stale-session retry are all exercised offline.
"""

from __future__ import annotations

import json

import pytest

from scripts.eval.mcp_client import McpClient, McpConfigError, McpRpcError

INIT_RESULT = {
    "jsonrpc": "2.0",
    "id": 1,
    "result": {
        "protocolVersion": "2024-11-05",
        "capabilities": {},
        "serverInfo": {"name": "lybrix-mcp", "version": "0.1.0"},
    },
}


def _rpc_result(payload) -> bytes:
    return json.dumps({"jsonrpc": "2.0", "id": 1, "result": {"content": [{"type": "text",
                                                                          "text": json.dumps(payload)}]}}).encode()


def make_fake_transport(script):
    """script: list of (status, headers, body) returned per POST in order."""
    calls = []

    def post_fn(url, headers, body_bytes):
        calls.append({"url": url, "headers": headers, "body": json.loads(body_bytes)})
        return script.pop(0) if script else (200, {}, b"")

    post_fn.calls = calls
    return post_fn


def make_client(script, monkeypatch=None):
    """Client with env-provided config (never read from the host env: pass
    url/token explicitly or monkeypatch)."""
    if monkeypatch is not None:
        monkeypatch.setenv("LYBRIX_MCP_URL", "http://test:8430/mcp")
        monkeypatch.setenv("LYBRIX_MCP_TOKEN", "tok")
        return McpClient(post_fn=make_fake_transport(script))
    return McpClient(post_fn=make_fake_transport(script), url="http://test:8430/mcp", token="tok")


# --- config ---------------------------------------------------------------


def test_missing_url_raises_naming_var(monkeypatch):
    monkeypatch.delenv("LYBRIX_MCP_URL", raising=False)
    monkeypatch.delenv("LYBRIX_MCP_TOKEN", raising=False)
    with pytest.raises(McpConfigError, match="LYBRIX_MCP_URL"):
        McpClient(post_fn=make_fake_transport([]))


def test_missing_token_raises_naming_var(monkeypatch):
    monkeypatch.setenv("LYBRIX_MCP_URL", "http://test:8430/mcp")
    monkeypatch.delenv("LYBRIX_MCP_TOKEN", raising=False)
    with pytest.raises(McpConfigError, match="LYBRIX_MCP_TOKEN"):
        McpClient(post_fn=make_fake_transport([]))


def test_custom_user_agent_sent():
    """Python-urllib/* is bot-blocked on the public URL — a custom UA is
    always sent."""
    post_fn = make_fake_transport([(200, {"mcp-session-id": "s1"}, json.dumps(INIT_RESULT).encode()),
                                   (202, {}, b"")])
    client = McpClient(post_fn=post_fn, url="http://test:8430/mcp", token="tok")
    client.handshake()
    assert post_fn.calls[0]["headers"]["User-Agent"] == "lybrix-eval/0.1.0"
    assert post_fn.calls[0]["headers"]["Authorization"] == "Bearer tok"


# --- handshake ------------------------------------------------------------


def test_handshake_captures_session_id_from_header():
    post_fn = make_fake_transport([(200, {"mcp-session-id": "sess-abc"}, json.dumps(INIT_RESULT).encode()),
                                   (202, {}, b"")])
    client = McpClient(post_fn=post_fn, url="http://test:8430/mcp", token="tok")
    client.handshake()
    assert client.session_id == "sess-abc"
    # second call is notifications/initialized and carries the session header
    assert post_fn.calls[1]["body"]["method"] == "notifications/initialized"
    assert post_fn.calls[1]["headers"]["mcp-session-id"] == "sess-abc"


def test_handshake_session_header_case_insensitive():
    post_fn = make_fake_transport([(200, {"Mcp-Session-Id": "sess-XYZ"}, json.dumps(INIT_RESULT).encode()),
                                   (202, {}, b"")])
    client = McpClient(post_fn=post_fn, url="http://test:8430/mcp", token="tok")
    client.handshake()
    assert client.session_id == "sess-XYZ"


def test_initialized_tolerates_empty_202():
    post_fn = make_fake_transport([(200, {"mcp-session-id": "s"}, json.dumps(INIT_RESULT).encode()),
                                   (202, {}, b"")])
    client = McpClient(post_fn=post_fn, url="http://test:8430/mcp", token="tok")
    client.handshake()  # must not raise on the empty 202


# --- body parsing ---------------------------------------------------------


def test_parse_body_sse_takes_first_data_line():
    raw = (b"event: message\n"
           b'data: {"jsonrpc": "2.0", "id": 1, "result": {"n": 1}}\n'
           b'data: {"jsonrpc": "2.0", "id": 2, "result": {"n": 2}}\n')
    payload = McpClient.parse_body(raw)
    assert payload["result"]["n"] == 1


def test_parse_body_plain_json():
    raw = json.dumps({"jsonrpc": "2.0", "id": 1, "result": {}}).encode()
    assert McpClient.parse_body(raw)["id"] == 1


def test_parse_body_empty_returns_none():
    assert McpClient.parse_body(b"") is None
    assert McpClient.parse_body(b"   \n") is None


def test_parse_result_text_double_decode():
    inner = json.dumps([{"doc_title": "Go in Action", "score": 0.5}])
    assert McpClient.parse_result_text(inner) == [{"doc_title": "Go in Action", "score": 0.5}]


def test_parse_result_text_falls_back_to_raw_string():
    assert McpClient.parse_result_text("not json {") == "not json {"


# --- tool calls -----------------------------------------------------------


def test_call_double_decodes_content_text():
    script = [(200, {"mcp-session-id": "s"}, json.dumps(INIT_RESULT).encode()),
              (202, {}, b""),
              (200, {}, _rpc_result([{"doc_title": "T"}]))]
    client = McpClient(post_fn=make_fake_transport(script), url="u", token="t")
    client.handshake()
    assert client.call("search", {"query": "q"}) == [{"doc_title": "T"}]


def test_stale_session_re_handshakes_once_and_retries():
    missing = json.dumps({"jsonrpc": "2.0", "id": 1,
                          "error": {"code": -32600, "message": "Missing session ID"}}).encode()
    script = [(200, {"mcp-session-id": "old"}, json.dumps(INIT_RESULT).encode()),
              (202, {}, b""),
              (200, {}, missing),
              (200, {"mcp-session-id": "new"}, json.dumps(INIT_RESULT).encode()),
              (202, {}, b""),
              (200, {}, _rpc_result("ok"))]
    post_fn = make_fake_transport(script)
    client = McpClient(post_fn=post_fn, url="u", token="t")
    client.handshake()
    assert client.call("search", {"query": "q"}) == "ok"
    assert client.session_id == "new"
    # exactly one re-handshake: 6 requests total (init, initialized, fail,
    # re-init, re-initialized, retried call)
    assert len(post_fn.calls) == 6


def test_stale_session_does_not_retry_twice():
    missing = json.dumps({"jsonrpc": "2.0", "id": 1,
                          "error": {"code": -32600, "message": "Missing session ID"}}).encode()
    script = [(200, {"mcp-session-id": "old"}, json.dumps(INIT_RESULT).encode()),
              (202, {}, b""),
              (200, {}, missing),
              (200, {"mcp-session-id": "new"}, json.dumps(INIT_RESULT).encode()),
              (202, {}, b""),
              (200, {}, missing)]
    client = McpClient(post_fn=make_fake_transport(script), url="u", token="t")
    client.handshake()
    with pytest.raises(McpRpcError, match="Missing session ID"):
        client.call("search", {"query": "q"})


def test_call_rpc_error_raises():
    err = json.dumps({"jsonrpc": "2.0", "id": 1,
                      "error": {"code": -32602, "message": "bad params"}}).encode()
    script = [(200, {"mcp-session-id": "s"}, json.dumps(INIT_RESULT).encode()),
              (202, {}, b""),
              (200, {}, err)]
    client = McpClient(post_fn=make_fake_transport(script), url="u", token="t")
    client.handshake()
    with pytest.raises(McpRpcError, match="bad params"):
        client.call("search", {})


def test_search_omits_collection_when_none():
    script = [(200, {"mcp-session-id": "s"}, json.dumps(INIT_RESULT).encode()),
              (202, {}, b""),
              (200, {}, _rpc_result([]))]
    post_fn = make_fake_transport(script)
    client = McpClient(post_fn=post_fn, url="u", token="t")
    client.handshake()
    client.search("q")
    arguments = post_fn.calls[2]["body"]["params"]["arguments"]
    assert arguments == {"query": "q", "top_k": 8}


def test_search_passes_collection_and_top_k():
    script = [(200, {"mcp-session-id": "s"}, json.dumps(INIT_RESULT).encode()),
              (202, {}, b""),
              (200, {}, _rpc_result([]))]
    post_fn = make_fake_transport(script)
    client = McpClient(post_fn=post_fn, url="u", token="t")
    client.handshake()
    client.search("q", collection="coll-id", top_k=5)
    arguments = post_fn.calls[2]["body"]["params"]["arguments"]
    assert arguments == {"query": "q", "top_k": 5, "collection": "coll-id"}


def test_list_collections_returns_list():
    script = [(200, {"mcp-session-id": "s"}, json.dumps(INIT_RESULT).encode()),
              (202, {}, b""),
              (200, {}, _rpc_result([{"id": "abc", "name": "909-corpus", "doc_count": 768}]))]
    client = McpClient(post_fn=make_fake_transport(script), url="u", token="t")
    client.handshake()
    assert client.list_collections() == [{"id": "abc", "name": "909-corpus", "doc_count": 768}]
