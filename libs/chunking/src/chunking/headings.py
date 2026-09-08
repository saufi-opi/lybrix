"""heading_path utilities (PRD §5 chunks.heading_path).

The chunker reconstructs the breadcrumb while walking markdown; these
helpers normalise and render it for storage and UI display.
"""

from __future__ import annotations

from collections.abc import Iterable

SEP = " > "


def normalize(path: Iterable[str]) -> tuple[str, ...]:
    """Strip empties/whitespace; dedupe consecutive repeats."""
    out: list[str] = []
    for part in path:
        t = (part or "").strip()
        if not t:
            continue
        if not out or out[-1] != t:
            out.append(t)
    return tuple(out)


def render(path: Iterable[str]) -> str:
    return SEP.join(normalize(path))
