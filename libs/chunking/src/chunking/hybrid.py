"""Heading-aware, token-budgeted chunking (PRD §6.4 step 2).

Splits on markdown ATX headings, packs each section into windows of
``max_tokens`` (measured by the injected ``tokenizer`` — a callable
str -> int; defaults to a whitespace word-count heuristic), and attaches
the reconstructed ``heading_path`` for citation. Overlap between shards
was already handled at stitch time; within-section windows are
contiguous, so the only duplicate source left is neighbour dedupe.
"""

from __future__ import annotations

import hashlib
import re
from dataclasses import dataclass

_HEADING_RE = re.compile(r"^(#{1,6})\s+(.+?)\s*$")
_FENCE_RE = re.compile(r"^(```|~~~)")


def whitespace_tokenizer(text: str) -> int:
    """Cheap word-count heuristic (books-rag convention): good enough to
    keep chunks in a reasonable ballpark, no tokenizer download."""
    return len(text.split())


@dataclass(frozen=True)
class Chunk:
    text: str
    seq: int
    chunk_hash: str
    token_count: int
    heading_path: tuple[str, ...]
    page_start: int | None = None
    page_end: int | None = None


def chunk_markdown(
    markdown: str,
    tokenizer=None,
    max_tokens: int = 512,
    min_tokens: int = 0,
) -> list[Chunk]:
    """Chunk ``markdown`` into ordered, heading-path-labelled Chunks.

    Returns [] for empty input. ``chunk_hash`` = sha256 of the normalised
    text — the idempotency key for Postgres and the Qdrant point id.
    """
    count = tokenizer or whitespace_tokenizer
    chunks: list[Chunk] = []

    for heading_path, body in _sections(markdown):
        words = body.split()
        if not words:
            continue
        # Greedy pack: accumulate words until the token budget would bust.
        window: list[str] = []
        window_tokens = 0
        for word in words:
            t = count(word)
            if window and window_tokens + t > max_tokens:
                _emit(chunks, heading_path, window, count)
                window = []
                window_tokens = 0
            window.append(word)
            window_tokens += t
        if window:
            _emit(chunks, heading_path, window, count, min_tokens=min_tokens)

    return chunks


def _emit(
    chunks: list[Chunk],
    heading_path: tuple[str, ...],
    words: list[str],
    count,
    min_tokens: int = 0,
) -> None:
    text = " ".join(words)
    if heading_path:
        text = "\n\n".join([heading_path[-1], text])
    if count(text) < min_tokens:
        return
    h = hashlib.sha256(" ".join(text.split()).encode("utf-8")).hexdigest()
    chunks.append(
        Chunk(
            text=text,
            seq=len(chunks),
            chunk_hash=h,
            token_count=count(text),
            heading_path=heading_path,
        )
    )


def _sections(markdown: str) -> list[tuple[tuple[str, ...], str]]:
    """Yield (heading_path, body) sections; fences protect headings."""
    sections: list[tuple[tuple[str, ...], str]] = []
    stack: list[tuple[int, str]] = []
    current: list[str] = []
    in_fence = False
    started = False

    def flush() -> None:
        nonlocal current
        if started:
            sections.append((tuple(t for _, t in stack), "\n".join(current)))
        current = []

    for line in markdown.splitlines():
        if _FENCE_RE.match(line.strip()):
            in_fence = not in_fence
            current.append(line)
            started = True
            continue
        m = None if in_fence else _HEADING_RE.match(line)
        if m:
            flush()
            started = True
            level = len(m.group(1))
            title = m.group(2).strip()
            while stack and stack[-1][0] >= level:
                stack.pop()
            stack.append((level, title))
        else:
            current.append(line)
            if line.strip():
                started = True
    flush()
    return sections
