"""Golden-set dataset format: JSONL, one query per line.

Schema per line:
    {"id": "exact-001", "query": "...",
     "expected_doc_titles": ["..."],     # required, list; hit if ANY matches
     "expected_headings": ["..."],       # optional list — bonus check
     "category": "exact",                # required: exact | conceptual | drift
     "notes": "free text"}

The loader validates schema and raises DatasetError with line numbers on
duplicate ids / unknown category / missing-or-empty expected_doc_titles.
It also enforces the seed-title contract: every expected title must normalize
to >= 8 space-stripped chars (see judge.titles_match — shorter titles can
never match, so shipping one would create a permanently unmatchable row).

Contract: a placeholder row must never be committed — the loader raising is
the backstop, not the workflow. All drift rows were filled from live
read_pages/list_documents verification before commit, and each row's notes
records which probe verified its titles.
"""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from pathlib import Path

from scripts.eval.judge import space_stripped_len

CATEGORIES = ("exact", "conceptual", "drift")


class DatasetError(ValueError):
    """Raised on schema violations; message carries the 1-based line number."""


@dataclass
class QueryCase:
    id: str
    query: str
    expected_doc_titles: list[str]
    category: str
    expected_headings: list[str] = field(default_factory=list)
    notes: str = ""


def _validate_row(row: dict, line_no: int) -> QueryCase:
    for required in ("id", "query", "category"):
        value = row.get(required)
        if not isinstance(value, str) or not value.strip():
            raise DatasetError(f"line {line_no}: missing or empty required field {required!r}")
    titles = row.get("expected_doc_titles")
    if not isinstance(titles, list) or not titles or not all(
        isinstance(t, str) and t.strip() for t in titles
    ):
        raise DatasetError(
            f"line {line_no}: expected_doc_titles must be a non-empty list of "
            f"non-empty strings"
        )
    category = row["category"]
    if category not in CATEGORIES:
        raise DatasetError(
            f"line {line_no}: unknown category {category!r} "
            f"(expected one of {', '.join(CATEGORIES)})"
        )
    for title in titles:
        if space_stripped_len(title) < 8:
            raise DatasetError(
                f"line {line_no}: expected title {title!r} normalizes to fewer than "
                f"8 space-stripped chars and can never match (see judge.titles_match); "
                f"fix the row"
            )
    headings = row.get("expected_headings", [])
    if not isinstance(headings, list) or not all(isinstance(h, str) for h in headings):
        raise DatasetError(f"line {line_no}: expected_headings must be a list of strings")
    notes = row.get("notes", "")
    if not isinstance(notes, str):
        raise DatasetError(f"line {line_no}: notes must be a string")
    return QueryCase(
        id=row["id"],
        query=row["query"],
        expected_doc_titles=titles,
        category=category,
        expected_headings=headings,
        notes=notes,
    )


def load_dataset(path: str | Path) -> list[QueryCase]:
    """Parse and validate a JSONL dataset; raises DatasetError on any bad row."""
    cases: list[QueryCase] = []
    seen: dict[str, int] = {}
    with open(path, encoding="utf-8") as handle:
        for line_no, line in enumerate(handle, start=1):
            text = line.strip()
            if not text:
                continue  # tolerate blank lines
            try:
                row = json.loads(text)
            except json.JSONDecodeError as exc:
                raise DatasetError(f"line {line_no}: invalid JSON: {exc}") from exc
            if not isinstance(row, dict):
                raise DatasetError(f"line {line_no}: each line must be a JSON object")
            case = _validate_row(row, line_no)
            if case.id in seen:
                raise DatasetError(
                    f"line {line_no}: duplicate id {case.id!r} (first seen line {seen[case.id]})"
                )
            seen[case.id] = line_no
            cases.append(case)
    if not cases:
        raise DatasetError(f"{path}: dataset is empty")
    return cases
