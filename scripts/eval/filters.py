r"""TOC / junk-heading hit filter — the toggleable heuristics.

Ported from the known defect classes observed on live probes:
- heading_path containing "table of contents" (the TOC trap: navigation text
  overmatches and is never citable),
- bare junk headings as the last heading_path entry: Index, See Also, Problem,
  Solution (exact last-entry match, case-insensitive),
- front-matter filename artifacts surfacing as doc_title: `^\d+-FM-`,
  `.indd`, `.dvi`, `.pdf` suffixes.

run.py's --junk-filter {on,off} toggles whether these are excluded from
metrics; the runner also records raw unfiltered metrics so one run quantifies
the filter's impact itself.
"""

from __future__ import annotations

import re

_JUNK_LAST_HEADINGS = {"index", "see also", "problem", "solution"}
_FRONT_MATTER_RE = re.compile(r"(^\d+-FM-|\.(?:indd|dvi|pdf)$)", re.IGNORECASE)


def is_junk(result: dict) -> bool:
    """True when a search result is TOC/junk noise that should not count as a
    retrieval hit (or against one) in filtered metrics."""
    heading_path = result.get("heading_path")
    if isinstance(heading_path, list) and heading_path:
        joined = " › ".join(str(part) for part in heading_path).lower()
        if "table of contents" in joined:
            return True
        last = str(heading_path[-1]).strip().lower()
        if last in _JUNK_LAST_HEADINGS:
            return True
    title = result.get("doc_title")
    return isinstance(title, str) and bool(_FRONT_MATTER_RE.search(title))
