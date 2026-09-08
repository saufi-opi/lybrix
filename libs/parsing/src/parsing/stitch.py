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
        docs.append(json.loads(fetch[idx]))
    return docs


def stitch(docs: list[dict[str, Any]]) -> dict[str, Any]:
    """Concatenate shard documents in order into a single doc-like dict.

    Works on Docling's serialised export shape ({"texts": [...], ...}),
    tolerating shards that produced no text blocks. Heading reconciliation
    is line-based: a shard's first markdown heading that equals the
    previous shard's last is dropped (overlap dedupe at the boundary).
    """
    if not docs:
        return {"texts": [], "markdown": ""}

    lines: list[str] = []
    for _i, doc in enumerate(docs):
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

    return {"texts": [], "markdown": "\n".join(lines).strip() + "\n"}


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
