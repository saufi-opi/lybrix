"""plan_batches (R-16): greedy token-budgeted batching extracted from
handle_embed — budget semantics unit-testable without transformers or TEI.

Fake tokenizer: word-count x 2 (deterministic token estimates).
"""

from __future__ import annotations

from dataclasses import dataclass

from workers.embedder import plan_batches


@dataclass
class FakeChunk:
    text: str
    token_count: int = 0


def fake_tok(text: str, add_special_tokens=False, truncation=False, max_length=None):
    """Word-count x 2 tokens; truncation hard-cuts to max_length words x 2."""
    words = text.split()
    if truncation and max_length is not None:
        words = words[: max_length // 2]
    return {"input_ids": [0] * (len(words) * 2)}


def fake_decode(ids):
    return " ".join(str(i) for i in ids)


class TokWithDecode:
    def __call__(self, text, **kw):
        return fake_tok(text, **kw)

    def decode(self, ids):
        return fake_decode(ids)


def _chunks(*texts: str) -> list[FakeChunk]:
    return [FakeChunk(text=t) for t in texts]


def test_batches_respect_ctx_budget():
    # 3 chunks of 100 tokens each, budget 250: no batch may exceed 250 tokens
    chunks = _chunks("w1 " * 50, "w2 " * 50, "w3 " * 50)  # 100 tokens each
    batches = plan_batches(chunks, TokWithDecode(), ctx_budget=250, batch_size=48)
    assert len(batches) == 2
    sizes = [len(b) for b in batches]
    assert sizes == [2, 1]


def test_oversized_chunk_hard_cut_to_budget():
    # 3000 tokens (1500 words) vs budget 500: the chunk is hard-cut
    big = FakeChunk(text="w " * 1500)
    batches = plan_batches([big], TokWithDecode(), ctx_budget=500, batch_size=48)
    assert len(batches) == 1
    kept = batches[0][0]
    # replacement keeps the dataclass shape; text was decoded from <=500 ids
    assert len(kept.text.split()) == 500  # 500 ids = 500 spaces-joined numbers
    assert kept.token_count == big.token_count  # token_count itself untouched


def test_embed_batch_size_cap_honored():
    # many tiny chunks vs batch_size 2: no batch carries more than 2
    chunks = _chunks("a", "b", "c", "d", "e")  # 2 tokens each
    batches = plan_batches(chunks, TokWithDecode(), ctx_budget=1900, batch_size=2)
    assert all(len(b) <= 2 for b in batches)
    assert len(batches) == 3


def test_budget_from_argument_not_constant():
    chunks = _chunks("w " * 100)  # 200 tokens
    small = plan_batches(chunks, TokWithDecode(), ctx_budget=100, batch_size=48)
    large = plan_batches(chunks, TokWithDecode(), ctx_budget=1000, batch_size=48)
    # budget 100: 200-token chunk hard-cut to 100 ids; budget 1000: untouched
    assert len(small[0][0].text.split()) == 100
    assert large[0][0].text == chunks[0].text
