"""Rerank (phase 2, PRD §7.1 step 3): candidate pool → cross-encoder via
tei-rerank → top-k. Graceful TeiUnavailable so callers fall back to the
un-reranked order rather than failing the query.

Live-measured TEI CPU constraints (nsspq Xeon, bge-reranker-base, 8 cores,
2026-09-19): the CPU backend caps a single rerank request at ~8-9 pairs —
larger client batches are 429-rejected instantly at the permit layer
("no permits available"), and >2 concurrent requests also 429. So the pool
is split into sub-batches of RERANK_BATCH (8) sent over RERANK_LANES (2)
parallel connections; 30 candidates ≈ 2.9s wall. RERANK_MAX_CHARS=800
bounds per-pair tokens (XLM-R-base ctx is 512 anyway — more chars are
wasted compute on this model).
"""

from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor

import httpx
from embedding.client import TeiUnavailable

RERANK_MAX_CHARS = 800
RERANK_BATCH = 8  # TEI CPU pair-ceiling; measured: 8-9 OK, 16+ per-request 429s
RERANK_LANES = 2  # parallel sub-batch requests; measured: 2 OK, 4 429s


def _rerank_batch(tei_rerank_url: str, query: str, texts: list[str], timeout_s: float) -> list[dict]:
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
    return resp.json()


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

    batches = [
        (offset, texts[offset : offset + RERANK_BATCH])
        for offset in range(0, len(texts), RERANK_BATCH)
    ]
    try:
        if len(batches) == 1:
            results = [(batches[0][0], _rerank_batch(tei_rerank_url, query, batches[0][1], timeout_s))]
        else:
            with ThreadPoolExecutor(max_workers=RERANK_LANES) as pool:
                futures = {
                    offset: pool.submit(_rerank_batch, tei_rerank_url, query, batch, timeout_s)
                    for offset, batch in batches
                }
                results = [(offset, f.result()) for offset, f in futures.items()]
    except (httpx.HTTPError, ValueError) as exc:
        raise TeiUnavailable(f"rerank failed: {exc}") from exc

    # merge: shift each sub-batch's local indices by its offset
    ranked = []
    for offset, batch_results in results:
        for r in batch_results:
            idx = int(r["index"]) + offset
            if 0 <= idx < len(texts):
                ranked.append((idx, float(r["score"])))
    ranked.sort(key=lambda t: t[1], reverse=True)
    return ranked[:top_k]
