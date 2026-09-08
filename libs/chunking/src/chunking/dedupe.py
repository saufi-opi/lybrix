"""Overlap removal (PRD §6.4 step 3): drop chunks whose hash duplicates
their neighbour — the residue of the splitter's 1-page overlap."""

from __future__ import annotations

from collections.abc import Iterable

from chunking.hybrid import Chunk


def drop_duplicate_neighbours(chunks: Iterable[Chunk]) -> list[Chunk]:
    """Keep order and original ``seq``; drop any chunk whose hash equals
    the immediately preceding kept chunk's hash."""
    out: list[Chunk] = []
    seen_prev: str | None = None
    for c in chunks:
        if c.chunk_hash == seen_prev:
            continue
        out.append(c)
        seen_prev = c.chunk_hash
    return out
