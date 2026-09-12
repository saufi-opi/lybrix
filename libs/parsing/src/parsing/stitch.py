"""Stitch shard JSONs back into one DoclingDocument (PRD §6.4 step 1).

Heading hierarchy must be reconciled across boundaries or every chunk
after shard 1 loses its section context. Docling serialises each shard
with its own heading tree restart, so we rebuild the document by
concatenating shard texts in order and dropping the duplicate leading
heading when a shard starts on the same heading the previous one ended
on (the 1-page overlap shows up here too).
"""

from __future__ import annotations

import json
from typing import Any


def load_shard_docs(
    fetch: dict[int, str],
) -> list[dict[str, Any]]:
    """Parse shard JSON payloads ``{idx: json_text}`` in idx order.

    Malformed shard JSON raises here — the embed job must not silently
    drop a shard's content; the retry ladder handles it upstream.
    """
    docs = []
    for idx in sorted(fetch):
        text = fetch[idx]
        try:
            docs.append(json.loads(text))
        except json.JSONDecodeError:
            # Parser uploads export_to_markdown() text directly — treat a
            # non-JSON payload as the markdown body itself.
            docs.append({"markdown": text})
    return docs


def stitch(
    docs: list[dict[str, Any]],
    shard_page_ranges: list[tuple[int, int]] | None = None,
) -> dict[str, Any]:
    """Concatenate shard documents in order into a single doc-like dict.

    Works on Docling's serialised export shape ({"texts": [...], ...}),
    tolerating shards that produced no text blocks. Heading reconciliation
    is line-based: a shard's first markdown heading that equals the
    previous shard's last is dropped (overlap dedupe at the boundary).

    ``shard_page_ranges`` (optional) is a list of ``(page_start, page_end)``
    1-based inclusive pairs aligned with ``docs`` (doc order = sorted shard
    idx). When given, the result carries ``"pages"``: a list mapping the
    0-based line index of the stitched markdown to a 1-based page number,
    interpolated proportionally within each shard. Ranges align to the
    docs that actually contributed lines — a shard with empty markdown is
    skipped together with its range so the map stays monotone.
    """
    if not docs:
        return {"texts": [], "markdown": ""}

    lines: list[str] = []
    page_of_line: list[int] = []
    # Pair each doc with its range in lockstep: a shard with empty markdown
    # contributes neither lines nor a consumed range, so the map stays
    # monotone even when some shards produced no text.
    paired = (
        zip(docs, shard_page_ranges, strict=True) if shard_page_ranges is not None else
        ((doc, None) for doc in docs)
    )
    for doc, page_range in paired:
        md = _markdown_of(doc)
        if not md:
            continue
        doc_lines = md.splitlines()
        if lines and doc_lines:
            # drop a duplicated boundary heading from the overlap
            prev_last = _last_heading(lines)
            first = _first_heading(doc_lines)
            if prev_last is not None and first == prev_last:
                doc_lines = doc_lines[1:]
                # and the blank line under it, if any
                while doc_lines and not doc_lines[0].strip():
                    doc_lines = doc_lines[1:]
        lines.extend(doc_lines)
        if page_range is not None:
            page_start, page_end = page_range
            n = len(doc_lines)
            span = page_end - page_start + 1
            for local in range(n):
                page_of_line.append(page_start + (local * span) // n)

    out: dict[str, Any] = {"texts": [], "markdown": "\n".join(lines).strip() + "\n"}
    if shard_page_ranges is not None:
        out["pages"] = page_of_line
    return out


def _markdown_of(doc: dict[str, Any]) -> str:
    md = doc.get("markdown")
    if isinstance(md, str):
        return md
    # Fall back to DoclingDocument export_text/markdown nested shapes.
    return ""


def _first_heading(lines: list[str]) -> str | None:
    for line in lines:
        s = line.strip()
        if s.startswith("#"):
            return s
    return None


def _last_heading(lines: list[str]) -> str | None:
    for line in reversed(lines):
        s = line.strip()
        if s.startswith("#"):
            return s
    return None
