"""Chunker contract (PRD §6.4): token budgets, heading paths, hashes."""

from __future__ import annotations

from chunking import chunk_markdown, drop_duplicate_neighbours
from chunking.headings import normalize, render


def test_empty_input():
    assert chunk_markdown("") == []
    assert chunk_markdown("   \n  ") == []


def test_respects_token_budget():
    md = "para " * 400  # 400 words
    chunks = chunk_markdown(md, tokenizer=lambda t: len(t.split()), max_tokens=100)
    assert all(c.token_count <= 100 for c in chunks)
    assert len(chunks) >= 4


def test_heading_path_attached():
    md = "# Part I\nintro text\n\n## 1.1 Caching\ncache text here\n"
    chunks = chunk_markdown(md)
    assert chunks[0].heading_path == ("Part I",)
    assert chunks[1].heading_path == ("Part I", "1.1 Caching")


def test_chunk_hash_is_stable_sha256():
    md = "some text to chunk"
    a = chunk_markdown(md)
    b = chunk_markdown(md)
    assert a[0].chunk_hash == b[0].chunk_hash
    assert len(a[0].chunk_hash) == 64


def test_seq_is_ordered_and_dense():
    md = "word " * 250
    chunks = chunk_markdown(md, max_tokens=50)
    assert [c.seq for c in chunks] == list(range(len(chunks)))


def test_fenced_code_does_not_split_headings():
    md = "# A\n```python\n# not a heading\n```\nbody\n"
    chunks = chunk_markdown(md)
    assert len(chunks) == 1
    assert chunks[0].heading_path == ("A",)


def test_dedupe_drops_consecutive_duplicates_only():
    from chunking import Chunk

    c1 = Chunk(text="a", seq=0, chunk_hash="h1", token_count=1, heading_path=())
    c2 = Chunk(text="dup", seq=1, chunk_hash="h1", token_count=1, heading_path=())
    c3 = Chunk(text="c", seq=2, chunk_hash="h2", token_count=1, heading_path=())
    kept = drop_duplicate_neighbours([c1, c2, c3])
    assert [k.chunk_hash for k in kept] == ["h1", "h2"]


def test_headings_normalize_and_render():
    assert normalize(["A", "", " A ", "B"]) == ("A", "B")
    assert render(["Part II", "Ch. 7", "7.3 Caching"]) == "Part II > Ch. 7 > 7.3 Caching"
