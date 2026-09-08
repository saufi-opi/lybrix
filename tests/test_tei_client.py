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

