"""Retrieval metrics over plain dicts: title normalization/matching, hit@k,
MRR, heading-path check, per-category breakdowns, junk filtering impact.

All functions are pure on plain dicts (the shapes produced by
mcp_client.McpClient.search) — directly unit-testable with no network.

Title matching is normative here because live probing showed many doc_title
values are messy: null, author-names ("Albert P. Malvino, Jerald A. Brown"),
filename-slugs ("cpluspluscrashcourse"). The judge must fuzzy-match titles.
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field

_NON_ALNUM = re.compile(r"[^a-z0-9\s]+")


def normalize_title(title: str) -> str:
    """Normalize a document title for matching.

    1. Fold symbol tokens IN PLACE, inserting no spaces: '+' -> 'plus' (so
       "C++" -> "cplusplus", adjacent — normative; "c plus plus" would break
       the pinned cpluspluscrashcourse case), '#' -> 'sharp', '.'/'_'/'-' ->
       space.
    2. Lowercase; replace every remaining non-alphanumeric char with space;
       collapse whitespace.
    """
    text = title.replace("+", "plus").replace("#", "sharp")
    text = text.replace(".", " ").replace("_", " ").replace("-", " ")
    text = _NON_ALNUM.sub(" ", text.lower())
    return " ".join(text.split())


def space_stripped_len(title: str) -> int:
    """Length of the space-stripped normal form — the loader's seed-title
    contract threshold (>= 8) is measured with this."""
    return len(normalize_title(title).replace(" ", ""))


def titles_match(a: str, b: str) -> bool:
    """True if normalized titles a and b match.

    Match = exact-equal normal forms, OR — after additionally removing ALL
    spaces from both normal forms — one is a substring of the other, with a
    length guard: the shorter space-stripped side must be >= 8 chars OR the
    match is exact-equal on the space-stripped forms.

    Space-stripping is what joins slugs to titles: "cpluspluscrashcourse" is
    a substring of "cpluspluscrashcourseafastpacedintroduction". The guard
    prevents "Go" (2 chars) from matching every title containing "go", while
    "Go in Action" (10 chars) still matches itself exactly and "Cloud Native
    Go" / "Get Programming with Go" pass the substring arm (13/20 chars).
    """
    a_norm, b_norm = normalize_title(a), normalize_title(b)
    if a_norm == b_norm:
        return True
    a_flat = a_norm.replace(" ", "")
    b_flat = b_norm.replace(" ", "")
    if len(a_flat) < 8 or len(b_flat) < 8:
        return False
    shorter, longer = sorted((a_flat, b_flat), key=len)
    return shorter in longer


def title_hit(expected_titles: list[str], results: list[dict]) -> int | None:
    """Rank (1-based) of the first result whose doc_title matches ANY expected
    title via titles_match; None if no result matches."""
    for rank, result in enumerate(results, start=1):
        title = result.get("doc_title")
        if not isinstance(title, str):
            continue  # doc_title can be null on live data
        if any(titles_match(title, expected) for expected in expected_titles):
            return rank
    return None


def heading_path_string(result: dict) -> str:
    """The join form heading checks run against: ' › '.join(heading_path)."""
    path = result.get("heading_path")
    if not isinstance(path, list):
        return ""
    return " › ".join(str(part) for part in path)


def heading_hit(expected_headings: list[str], result: dict) -> bool:
    """Case-insensitive substring check of expected headings against the
    joined heading_path — only meaningful on the title-matching hit; records
    whether the query landed in the right section, not just the right book."""
    if not expected_headings:
        return False
    path = heading_path_string(result).lower()
    return any(expected.lower() in path for expected in expected_headings)


@dataclass
class CategoryStats:
    queries: int = 0
    hits_at_1: int = 0
    hits_at_3: int = 0
    hits_at_8: int = 0
    reciprocal_ranks: list[float] = field(default_factory=list)
    heading_hits: int = 0

    @property
    def mrr(self) -> float:
        if not self.reciprocal_ranks:
            return 0.0
        return sum(self.reciprocal_ranks) / len(self.reciprocal_ranks)

    @property
    def hit_at_1(self) -> float:
        return self.hits_at_1 / self.queries if self.queries else 0.0

    @property
    def hit_at_3(self) -> float:
        return self.hits_at_3 / self.queries if self.queries else 0.0

    @property
    def hit_at_8(self) -> float:
        return self.hits_at_8 / self.queries if self.queries else 0.0


@dataclass
class Summary:
    """Aggregated metrics over one dataset run. `evaluated` counts only
    queries with a non-error, non-empty search response; error queries are
    tracked separately so run.py can fail loudly on them."""

    total_queries: int = 0
    evaluated: int = 0
    errors: int = 0
    empty_responses: int = 0
    overall: CategoryStats = field(default_factory=CategoryStats)
    by_category: dict[str, CategoryStats] = field(default_factory=dict)
    junk_hits_top8_raw: int = 0  # junk-flagged results seen in raw top-8
    junk_slots_top8_raw: int = 0  # total raw top-8 slots seen (denominator)


def _accumulate(stats: CategoryStats, rank: int | None, heading_ok: bool) -> None:
    stats.queries += 1
    if rank is not None:
        stats.reciprocal_ranks.append(1.0 / rank)
        if rank == 1:
            stats.hits_at_1 += 1
        if rank <= 3:
            stats.hits_at_3 += 1
        if rank <= 8:
            stats.hits_at_8 += 1
        if heading_ok:
            stats.heading_hits += 1


def evaluate(
    rows: list[dict],
    dataset: dict[str, dict],
    junk_filter=None,
) -> Summary:
    """Aggregate per-query judge rows into a Summary.

    rows: per-query records as produced by run.py — each has the dataset
    fields (id, category), the ranked `results` list, and optional `error`.
    dataset: {id -> QueryCase} for expected titles/headings.
    junk_filter: optional callable(result) -> bool; when given, junk results
    are excluded from the ranked metrics (raw metrics keep them).
    """
    summary = Summary(total_queries=len(rows))
    for row in rows:
        if row.get("error"):
            summary.errors += 1
            continue
        case = dataset[row["id"]]
        results = row.get("results") or []
        stats = summary.by_category.setdefault(row["category"], CategoryStats())
        if not results:
            summary.empty_responses += 1
            _accumulate(stats, None, False)  # counts as a miss in its category
            continue
        raw_results = row.get("raw_results") or results
        summary.junk_slots_top8_raw += min(len(raw_results), 8)
        summary.junk_hits_top8_raw += sum(
            1 for result in raw_results[:8] if junk_filter and junk_filter(result)
        )
        ranked = [r for r in results if not (junk_filter and junk_filter(r))] if junk_filter else results
        rank = title_hit(case["expected_doc_titles"], ranked)
        hit_result = ranked[rank - 1] if rank is not None else None
        heading_ok = bool(hit_result) and heading_hit(case.get("expected_headings", []), hit_result)
        _accumulate(summary.overall, rank, heading_ok)
        _accumulate(stats, rank, heading_ok)
    summary.evaluated = summary.overall.queries
    return summary
