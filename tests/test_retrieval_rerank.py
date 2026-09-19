"""Rerank stage (Phase 2): rerank() stub contract with a monkeypatched
httpx.post, search_impl wiring (flag off byte-identity / flag on rerank /
TeiUnavailable fallback / fetch-limit decoupling / score semantics), and the
Settings validator cases — all fake-based, no network.
"""

from __future__ import annotations

import logging

import httpx
import pytest
from embedding.client import TeiUnavailable
from retrieval.rerank import RERANK_MAX_CHARS, rerank


class _FakeResponse:
    def __init__(self, payload, status=200):
        self._payload = payload
        self.status_code = status

    def raise_for_status(self):
        if self.status_code >= 400:
            raise httpx.HTTPStatusError(
                f"status {self.status_code}", request=None, response=None
            )

    def json(self):
        if isinstance(self._payload, Exception):
            raise self._payload
        return self._payload


# --- rerank() stub contract --------------------------------------------------


def test_rerank_returns_sorted_desc(monkeypatch):
    captured = {}

    def fake_post(url, json=None, timeout=None):
        captured.update(url=url, body=json, timeout=timeout)
        # index 2 best, then 0, then 1 (unsorted on the wire)
        return _FakeResponse(
            [{"index": 0, "score": 0.5}, {"index": 2, "score": 0.9}, {"index": 1, "score": 0.7}]
        )

    monkeypatch.setattr(httpx, "post", fake_post)
    ranked = rerank("http://x:8083", "q", ["a", "b", "c"], top_k=8, timeout_s=5.0)
    assert ranked == [(2, 0.9), (1, 0.7), (0, 0.5)]
    assert captured["url"] == "http://x:8083/rerank"
    assert captured["body"]["raw_scores"] is False
    assert captured["timeout"] == 5.0


def test_rerank_truncates_to_top_k(monkeypatch):
    monkeypatch.setattr(
        httpx,
        "post",
        lambda *a, **kw: _FakeResponse(
            [{"index": i, "score": 1.0 - i / 10} for i in range(5)]
        ),
    )
    ranked = rerank("http://x", "q", ["a", "b", "c", "d", "e"], top_k=2)
    assert ranked == [(0, 1.0), (1, 0.9)]


def test_rerank_filters_out_of_range_indices(monkeypatch):
    monkeypatch.setattr(
        httpx,
        "post",
        lambda *a, **kw: _FakeResponse(
            [{"index": 0, "score": 0.5}, {"index": 7, "score": 0.9}, {"index": -1, "score": 0.8}]
        ),
    )
    ranked = rerank("http://x", "q", ["only"], top_k=8)
    assert ranked == [(0, 0.5)]


def test_rerank_empty_texts_no_http_call(monkeypatch):
    def boom(*a, **kw):
        raise AssertionError("must not hit HTTP on empty texts")

    monkeypatch.setattr(httpx, "post", boom)
    assert rerank("http://x", "q", []) == []


def test_rerank_http_error_raises_tei_unavailable(monkeypatch):
    def boom(*a, **kw):
        raise httpx.ConnectError("connection refused")

    monkeypatch.setattr(httpx, "post", boom)
    with pytest.raises(TeiUnavailable, match="rerank failed"):
        rerank("http://x", "q", ["a"])


def test_rerank_bad_status_raises_tei_unavailable(monkeypatch):
    monkeypatch.setattr(
        httpx, "post", lambda *a, **kw: _FakeResponse([], status=503)
    )
    with pytest.raises(TeiUnavailable):
        rerank("http://x", "q", ["a"])


def test_rerank_bad_json_raises_tei_unavailable(monkeypatch):
    monkeypatch.setattr(
        httpx, "post", lambda *a, **kw: _FakeResponse(ValueError("not json"))
    )
    with pytest.raises(TeiUnavailable):
        rerank("http://x", "q", ["a"])


def test_rerank_truncates_texts_before_post(monkeypatch):
    """Chunk texts run to ~6000 chars; the POST body must carry the
    2000-char cut (CPU latency budget), indices still map 1:1."""
    captured = {}

    def fake_post(url, json=None, timeout=None):
        captured.update(body=json)
        return _FakeResponse([{"index": 0, "score": 0.4}, {"index": 1, "score": 0.3}])

    monkeypatch.setattr(httpx, "post", fake_post)
    long_text = "x" * (RERANK_MAX_CHARS + 500)
    short_text = "short"
    ranked = rerank("http://x", "q", [long_text, short_text])
    assert ranked == [(0, 0.4), (1, 0.3)]
    sent = captured["body"]["texts"]
    assert len(sent[0]) == RERANK_MAX_CHARS  # the 2000-char cut
    assert sent[1] == short_text


# --- search_impl wiring (fakes, no network) -----------------------------------

from mcp_server.server import search_impl  # noqa: E402

from tests.helpers import FakeSession  # noqa: E402


class _EmptyResult:
    def scalars(self):
        return []


class _CaptureSession(FakeSession):
    def execute(self, stmt):
        return _EmptyResult()


class FakeQdrantNoop:
    def query_points(self, **kw):
        pass  # not reached: hybrid_search is faked out


def _hit(i, text, score):
    """A hydrated SearchHit-shaped row straight from hybrid_search."""
    import uuid

    from retrieval.search import SearchHit

    return SearchHit(
        chunk_id=uuid.uuid4(),
        doc_id=uuid.uuid4(),
        doc_title=f"doc {i}",
        page_start=1,
        page_end=2,
        heading_path=[],
        text=text,
        score=score,
    )


@pytest.fixture
def capture(monkeypatch):
    """Patch hybrid_search + rerank; return (captured, hybrid_hits_setter,
    rerank_calls, rerank_result_setter)."""
    captured: dict = {"hybrid": {}, "rerank_calls": []}
    state: dict = {"hybrid_hits": [], "rerank_result": None, "rerank_raises": None}

    def fake_hybrid_search(qdrant, collection, session, dense_query, sparse_query,
                           top_k, collection_id=None, doc_id=None):
        captured["hybrid"].update(top_k=top_k, sparse_query=sparse_query)
        return state["hybrid_hits"]

    def fake_rerank(url, query, texts, top_k=8, timeout_s=5.0):
        captured["rerank_calls"].append(
            {"url": url, "query": query, "texts": texts, "top_k": top_k,
             "timeout_s": timeout_s}
        )
        if state["rerank_raises"] is not None:
            raise state["rerank_raises"]
        return state["rerank_result"]

    monkeypatch.setattr("retrieval.search.hybrid_search", fake_hybrid_search)
    monkeypatch.setattr("retrieval.rerank.rerank", fake_rerank)
    return captured, state


def _settings(**overrides):
    from tests.conftest import make_settings

    return make_settings(**overrides)


def test_search_impl_flag_off_no_rerank_byte_identical(capture):
    """Flag off: hybrid_search called with top_k=limit, rerank NOT called,
    scores untouched (byte-identical regression guard)."""
    captured, state = capture
    state["hybrid_hits"] = [_hit(0, "t0", 0.9), _hit(1, "t1", 0.5)]
    settings = _settings(rerank_enabled=False)

    out = search_impl(_CaptureSession(), FakeQdrantNoop(), lambda q: [0.1], settings, "q", top_k=8)

    assert captured["hybrid"]["top_k"] == 8  # fetch_k == limit
    assert captured["rerank_calls"] == []  # never called
    assert [item["score"] for item in out] == [0.9, 0.5]  # RRF scores preserved
    assert [item["text"] for item in out] == ["t0", "t1"]


def test_search_impl_flag_on_reranks_and_overwrites_scores(capture):
    """Flag on: fetch_k = rerank_candidates when > limit; rerank called with
    the fetched candidates' texts; scores become rerank scores and positions
    agree; result truncated back to the tool's top_k."""
    captured, state = capture
    hits = [_hit(i, f"text-{i}", 0.1 * (10 - i)) for i in range(8)]  # 8 fetched
    state["hybrid_hits"] = hits
    state["rerank_result"] = [(3, 0.99), (0, 0.95), (7, 0.9), (1, 0.8), (5, 0.7),
                              (2, 0.6), (6, 0.5), (4, 0.4)]
    settings = _settings(rerank_enabled=True, rerank_candidates=30, top_k_user=8) if False else \
        _settings(rerank_enabled=True, rerank_candidates=30)

    out = search_impl(_CaptureSession(), FakeQdrantNoop(), lambda q: [0.1], settings, "q", top_k=8)

    assert captured["hybrid"]["top_k"] == 30  # fetch decoupled from limit
    call = captured["rerank_calls"][0]
    assert call["texts"] == [h.text for h in hits]
    assert call["top_k"] == 8  # len(hits) — the full pool, caller truncates
    assert call["url"] == settings.tei_rerank_url
    # order follows rerank; scores are the rerank scores
    assert [item["text"] for item in out] == [f"text-{i}" for i in (3, 0, 7, 1, 5, 2, 6, 4)]
    assert [item["score"] for item in out] == [0.99, 0.95, 0.9, 0.8, 0.7, 0.6, 0.5, 0.4]


def test_search_impl_tei_unavailable_falls_back(capture, caplog):
    """TeiUnavailable -> un-reranked order, warning logged, RRF scores
    preserved, result still truncated to limit."""
    captured, state = capture
    hits = [_hit(i, f"text-{i}", 0.1 * (10 - i)) for i in range(5)]
    state["hybrid_hits"] = hits
    state["rerank_raises"] = TeiUnavailable("rerank down")
    settings = _settings(rerank_enabled=True, rerank_candidates=30)

    with caplog.at_level(logging.WARNING, logger="mcp_server.server"):
        out = search_impl(_CaptureSession(), FakeQdrantNoop(), lambda q: [0.1], settings, "q", top_k=3)

    assert [item["text"] for item in out] == [h.text for h in hits[:3]]  # original order
    assert [item["score"] for item in out] == [1.0, 0.9, 0.8]  # original RRF scores
    assert any("rerank unavailable" in r.message for r in caplog.records)


def test_search_impl_rerank_single_hit_skipped(capture):
    """len(hits) <= 1: nothing to reorder — no rerank call, no fetch bump
    interplay (still fetch_k when flag on; single hit passes through)."""
    captured, state = capture
    state["hybrid_hits"] = [_hit(0, "only", 0.5)]
    settings = _settings(rerank_enabled=True, rerank_candidates=30)

    out = search_impl(_CaptureSession(), FakeQdrantNoop(), lambda q: [0.1], settings, "q", top_k=8)

    assert captured["rerank_calls"] == []
    assert [item["score"] for item in out] == [0.5]


def test_fetch_limit_decoupling_top_k_25(capture):
    """User top_k=25 (== search_max_top_k) with flag on: fetch stays 30,
    returns 25."""
    captured, state = capture
    hits = [_hit(i, f"t{i}", float(i)) for i in range(30)]
    state["hybrid_hits"] = hits
    state["rerank_result"] = [(i, 1.0 - i / 100) for i in range(30)]  # identity order
    settings = _settings(rerank_enabled=True, rerank_candidates=30)

    out = search_impl(_CaptureSession(), FakeQdrantNoop(), lambda q: [0.1], settings, "q", top_k=25)

    assert captured["hybrid"]["top_k"] == 30  # max(25, 30)
    assert len(out) == 25  # tool contract: exactly the requested top_k


def test_fetch_limit_decoupling_top_k_8(capture):
    """User top_k=8, rerank_candidates=30: fetch 30, return 8."""
    captured, state = capture
    hits = [_hit(i, f"t{i}", float(i)) for i in range(30)]
    state["hybrid_hits"] = hits
    state["rerank_result"] = [(i, 1.0 - i / 100) for i in range(30)]
    settings = _settings(rerank_enabled=True, rerank_candidates=30)

    out = search_impl(_CaptureSession(), FakeQdrantNoop(), lambda q: [0.1], settings, "q", top_k=8)

    assert captured["hybrid"]["top_k"] == 30
    assert len(out) == 8


def test_settings_validator_requires_url_when_flag_on():
    # pydantic wraps the model_validator's SettingsError in a ValidationError
    # (both are ValueError subclasses) — assert on the message content.
    with pytest.raises(ValueError, match="RERANK_ENABLED=true requires TEI_RERANK_URL"):
        _settings(rerank_enabled=True, tei_rerank_url="")


def test_settings_validator_ok_with_url():
    settings = _settings(rerank_enabled=True, tei_rerank_url="http://x:8083")
    assert settings.rerank_enabled is True
