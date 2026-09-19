"""Rerank (phase 2, PRD §7.1 step 3): top-50 → cross-encoder via
tei-rerank → top-8. Stubbed behind the same TeiClient-style surface so
M3 wires it without touching callers."""

from __future__ import annotations

import httpx
from embedding.client import TeiUnavailable

# CPU latency budget: ~30 candidates × 2000 chars keeps the request under
# the <500ms/query p50 target on 4 cores (the model's 8194-token context
# would allow far more but costs 4-core time; if conceptual-category
# regressions show up in the phase2-rerank eval, revisit this cap FIRST as
# the cheapest quality lever).
RERANK_MAX_CHARS = 2000


def rerank(
    tei_rerank_url: str,
    query: str,
    texts: list[str],
    top_k: int = 8,
    timeout_s: float = 5.0,
) -> list[tuple[int, float]]:
    """Return [(original_index, score)] best-first, truncated to top_k.

    Raises TeiUnavailable on any failure — callers fall back to the
    un-reranked order rather than failing the query.
    """
    if not texts:
        return []
    try:
        resp = httpx.post(
            f"{tei_rerank_url.rstrip('/')}/rerank",
            # truncation happens before the HTTP boundary so the returned
            # indices still map 1:1 to the caller's original list
            json={
                "query": query,
                "texts": [t[:RERANK_MAX_CHARS] for t in texts],
                "raw_scores": False,
            },
            timeout=timeout_s,
        )
        resp.raise_for_status()
        results = resp.json()
    except (httpx.HTTPError, ValueError) as exc:
        raise TeiUnavailable(f"rerank failed: {exc}") from exc

    ranked = [
        (int(r["index"]), float(r["score"]))
        for r in results
        if 0 <= int(r["index"]) < len(texts)
    ]
    ranked.sort(key=lambda t: t[1], reverse=True)
    return ranked[:top_k]
