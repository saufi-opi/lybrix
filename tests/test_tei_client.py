"""TEI client resilience (PRD §6.4): backoff/breaker via a fake transport."""

from __future__ import annotations

import httpx
import pytest
from embedding.client import CircuitOpen, TeiClient, TeiUnavailable


class StubTransport(httpx.BaseTransport):
    """Returns canned responses per call; counts requests."""

    def __init__(self, responses: list[httpx.Response]):
        self.responses = list(responses)
        self.calls = 0

    def handle_request(self, request: httpx.Request) -> httpx.Response:
        self.calls += 1
        return self.responses.pop(0)


def _client(responses, retries=3, breaker=5) -> TeiClient:
    c = TeiClient("http://tei.test", max_retries=retries, breaker_threshold=breaker)
    transport = StubTransport(responses)
    c._client = httpx.Client(transport=transport)
    c._transport = transport
    return c


def test_embed_success_first_try():
    c = _client([httpx.Response(200, json=[[0.1, 0.2]])])
    assert c.embed(["hello"]) == [[0.1, 0.2]]
    assert c._consecutive_failures == 0
    c.close()


def test_embed_retries_on_503_then_succeeds(monkeypatch):
    monkeypatch.setattr("time.sleep", lambda s: None)
    c = _client(
        [httpx.Response(503), httpx.Response(503), httpx.Response(200, json=[[1.0]])],
        retries=3,
    )
    assert c.embed(["x"]) == [[1.0]]
    assert c._consecutive_failures == 0
    c.close()


def test_embed_gives_up_after_max_retries(monkeypatch):
    monkeypatch.setattr("time.sleep", lambda s: None)
    c = _client([httpx.Response(503)] * 4, retries=3)
    with pytest.raises(TeiUnavailable):
        c.embed(["x"])
    c.close()


def test_circuit_opens_after_consecutive_failures(monkeypatch):
    monkeypatch.setattr("time.sleep", lambda s: None)
    # 3 attempts in one call, breaker at 3 → the call dies with CircuitOpen
    # mid-flight, and every later call refuses without touching transport.
    c = _client([httpx.Response(503)] * 10, retries=2, breaker=3)
    with pytest.raises(CircuitOpen):
        c.embed(["x"])
    assert c._transport.calls == 3
    with pytest.raises(CircuitOpen):
        c.embed(["x"])
    assert c._transport.calls == 3  # unchanged: refused locally
    c.close()


def test_embed_4xx_fails_fast_without_retry():
    c = _client([httpx.Response(422, text="bad input")], retries=3)
    with pytest.raises(TeiUnavailable):
        c.embed(["x"])
    assert c._transport.calls == 1  # no retry on caller bug
    c.close()


def test_embed_normalizes_ollama_envelope_exactly():
    c = _client([httpx.Response(200, json={"embeddings": [[0.5, 0.6]]})])
    assert c.embed(["hello"]) == [[0.5, 0.6]]
    c.close()


def test_embed_ollama_multi_input_order_preserved():
    payload = {"embeddings": [[0.1], [0.2], [0.3]]}
    c = _client([httpx.Response(200, json=payload)])
    c._backend = "ollama"
    c._model = "bge-m3"
    assert c.embed(["a", "b", "c"]) == [[0.1], [0.2], [0.3]]
    c.close()


def test_ollama_uses_api_embed_and_model_payload():
    class RoutedTransport(httpx.BaseTransport):
        def handle_request(self, request: httpx.Request) -> httpx.Response:
            assert request.url.path == "/api/embed"
            import json
            body = json.loads(request.content)
            assert body == {"model": "bge-m3", "input": ["hello"]}
            return httpx.Response(200, json={"embeddings": [[0.1, 0.2]]})

    c = TeiClient("http://jetson:11434", backend="ollama", model="bge-m3")
    c._client = httpx.Client(transport=RoutedTransport())
    assert c.embed(["hello"]) == [[0.1, 0.2]]
    c.close()


def test_unsupported_embed_backend_rejected():
    with pytest.raises(ValueError, match="unsupported EMBED_BACKEND"):
        TeiClient("http://x", backend="invalid")



def test_health_falls_back_to_root_for_ollama():
    # ollama has no /health; GET / returns 200. health() must try /health
    # first, then / so an ollama backend doesn't report "down".
    class RoutedTransport(httpx.BaseTransport):
        def __init__(self):
            self.calls: list[str] = []

        def handle_request(self, request: httpx.Request) -> httpx.Response:
            self.calls.append(request.url.path)
            if request.url.path == "/":
                return httpx.Response(200)
            return httpx.Response(404)

    t = RoutedTransport()
    c = TeiClient("http://tei.test")
    c._client = httpx.Client(transport=t)
    assert c.health() is True
    assert t.calls == ["/health", "/"]  # TEI first, ollama fallback
    c.close()
