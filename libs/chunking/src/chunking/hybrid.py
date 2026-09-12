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
    pages: list[int] | None = None,
) -> list[Chunk]:
    """Chunk ``markdown`` into ordered, heading-path-labelled Chunks.

    Returns [] for empty input. ``chunk_hash`` = sha256 of the normalised
    text — the idempotency key for Postgres and the Qdrant point id.

    ``pages`` (optional) maps the 0-based line index of ``markdown`` to a
    1-based page number (built at stitch time). When given, chunks carry
    ``page_start``/``page_end``; when absent they stay ``None`` (text and
    therefore chunk hashes are unchanged either way).
    """
    count = tokenizer or whitespace_tokenizer
    chunks: list[Chunk] = []

    for heading_path, body, start_line, end_line in _sections(markdown):
        words = body.split()
        if not words:
            continue
        # Greedy pack: accumulate words until the token budget would bust.
        window: list[str] = []
        window_tokens = 0
        for word in words:
            t = count(word)
            if window and window_tokens + t > max_tokens:
                _emit(chunks, heading_path, window, count, pages, start_line, end_line)
                window = []
                window_tokens = 0
            window.append(word)
            window_tokens += t
        if window:
            _emit(
                chunks,
                heading_path,
                window,
                count,
                pages,
                start_line,
                end_line,
                min_tokens=min_tokens,
            )

    return chunks


def _emit(
    chunks: list[Chunk],
    heading_path: tuple[str, ...],
    words: list[str],
    count,
    pages: list[int] | None = None,
    start_line: int = 0,
    end_line: int = 0,
    min_tokens: int = 0,
) -> None:
    text = " ".join(words)
    if heading_path:
        text = "\n\n".join([heading_path[-1], text])
    if count(text) < min_tokens:
        return
    h = hashlib.sha256(" ".join(text.split()).encode("utf-8")).hexdigest()
    page_start = pages[start_line] if pages else None
    page_end = pages[end_line] if pages else None
    chunks.append(
        Chunk(
            text=text,
            seq=len(chunks),
            chunk_hash=h,
            token_count=count(text),
            heading_path=heading_path,
            page_start=page_start,
            page_end=page_end,
        )
    )


def _sections(markdown: str) -> list[tuple[tuple[str, ...], str, int, int]]:
    """Yield (heading_path, body, start_line, end_line) sections; fences
    protect headings. Line indices are into ``markdown.splitlines()`` —
    the same indexing stitch's page map uses."""
    sections: list[tuple[tuple[str, ...], str, int, int]] = []
    stack: list[tuple[int, str]] = []
    current: list[str] = []
    in_fence = False
    started = False
    start_line = 0
    line_no = -1

    def flush(end_line: int) -> None:
        nonlocal current
        if started:
            sections.append((tuple(t for _, t in stack), "\n".join(current), start_line, end_line))
        current = []

    last_line_no = -1
    for line_no, line in enumerate(markdown.splitlines()):
        last_line_no = line_no
        if _FENCE_RE.match(line.strip()):
            in_fence = not in_fence
            current.append(line)
            started = True
            continue
        m = None if in_fence else _HEADING_RE.match(line)
        if m:
            # section body ends on the last line BEFORE this heading
            flush(max(line_no - 1, 0))
            started = True
            start_line = line_no
            level = len(m.group(1))
            title = m.group(2).strip()
            while stack and stack[-1][0] >= level:
                stack.pop()
            stack.append((level, title))
        else:
            current.append(line)
            if line.strip():
                started = True
                if not current[:-1] or not any(current[:-1]):
                    start_line = line_no
    flush(last_line_no)
    return sections
