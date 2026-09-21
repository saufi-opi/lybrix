"""Runner CLI: golden-set dataset x live MCP endpoint -> results JSON +
human summary MD.

    uv run python -m scripts.eval.run \
      --dataset scripts/eval/datasets/seed.jsonl \
      --top-k 8 --label baseline --junk-filter on
      # optional: --collection <id-or-name> — resolved to the collection id

Only works from repo root (pytest's prepend import mode / `python -m` cwd
put the repo root on sys.path); there is no sys.path hack — run it from the
root. See scripts/eval/README.md.

Exit codes: 0 on a clean run; non-zero when any query raised, or when <50% of
queries returned ANY result rows (endpoint misconfig / auth / collection
typo). Low hit-rate is a finding, not a crash — quality failures never change
the exit code.
"""

from __future__ import annotations

import argparse
import datetime as _dt
import json
import sys
import time
from pathlib import Path

from scripts.eval import filters as filters_mod
from scripts.eval import judge
from scripts.eval.dataset import load_dataset
from scripts.eval.mcp_client import McpClient, McpConfigError, McpRpcError

RESULTS_DIR = Path(__file__).resolve().parent / "results"
# Verified pattern from the Hermes client: 0.3 s pacing between queries
# (42 queries in ~25 s).
QUERY_PACING_S = 0.3
# Loud-failure threshold: fewer than this fraction of queries returning any
# result rows means endpoint misconfig/auth/collection typo — fail loudly.
MIN_RESULT_FRACTION = 0.5


def any_results_rate(rows: list[dict]) -> float:
    """Fraction of non-error queries whose search returned at least one row.
    The <50% loud-failure predicate; unit-tested in tests/test_eval_judge.py."""
    eligible = [row for row in rows if not row.get("error")]
    if not eligible:
        return 0.0
    return sum(1 for row in eligible if row.get("results")) / len(eligible)


def resolve_collection(client: McpClient, value: str) -> str:
    """Resolve a --collection value to the collection ID that the search
    tool's payload filter actually matches on.

    The MCP `search` tool's `collection` argument filters on the Qdrant
    payload field `collection_id`, which carries the Collection *id* (a
    Postgres row id), not the Qdrant collection name ("chunks", hardcoded)
    and not necessarily the Collection name. A name that differs from its id
    would pass a names-based check yet silently filter every query to zero
    rows — so only the id ever reaches the search call: a value matching an
    id is sent as-is; a value matching a name is resolved to that id.
    """
    collections = client.list_collections()
    by_id = {str(c.get("id")): c for c in collections}
    if value in by_id:
        return value
    for collection in collections:
        if collection.get("name") == value:
            return str(collection["id"])
    valid_ids = sorted(by_id)
    valid_names = sorted(str(c.get("name")) for c in collections if c.get("name"))
    raise SystemExit(
        f"error: --collection {value!r} matches no collection id or name.\n"
        f"valid ids:   {', '.join(valid_ids) or '(none)'}\n"
        f"valid names: {', '.join(valid_names) or '(none)'}"
    )


def run_query(client: McpClient, case, top_k: int, collection_id: str | None) -> dict:
    """Search one query and judge it. Junk-flagged results stay in
    raw_results but are excluded from the filtered `results` ranking."""
    started = time.monotonic()
    try:
        raw_results = client.search(case.query, collection=collection_id, top_k=top_k)
    except (McpRpcError, McpConfigError) as exc:
        return {"id": case.id, "query": case.query, "error": f"{type(exc).__name__}: {exc}"}
    results = [result for result in raw_results if not filters_mod.is_junk(result)]
    rank = judge.title_hit(case.expected_doc_titles, results)
    hit_result = results[rank - 1] if rank is not None else None
    return {
        "id": case.id,
        "query": case.query,
        "category": case.category,
        "raw_results": raw_results[:top_k],
        "results": results[:top_k],
        "hit_rank": rank,
        "hit_title": hit_result.get("doc_title") if hit_result else None,
        "heading_hit": bool(hit_result) and judge.heading_hit(case.expected_headings, hit_result),
        "elapsed_s": round(time.monotonic() - started, 3),
    }


def _fmt_pct(numerator: int, denominator: int) -> str:
    return f"{100.0 * numerator / denominator:.0f}%" if denominator else "n/a"


def render_summary_md(label: str, rows: list[dict], summary: judge.Summary, top_k: int) -> str:
    """Human-readable run summary: hit@k / MRR, per-category table, junk-filter
    impact, per-query one-liners."""
    out: list[str] = []
    overall = summary.overall
    out.append(f"# Eval run: {label}")
    out.append("")
    out.append(f"- Queries: {summary.total_queries} "
               f"(evaluated {summary.evaluated}, errors {summary.errors}, "
               f"empty {summary.empty_responses})")
    out.append(f"- top_k: {top_k}, junk-filter: on")
    out.append(f"- hit@1 {_fmt_pct(overall.hits_at_1, overall.queries)}, "
               f"hit@3 {_fmt_pct(overall.hits_at_3, overall.queries)}, "
               f"hit@{top_k} {_fmt_pct(overall.hits_at_8, overall.queries)}, "
               f"MRR {overall.mrr:.3f}")
    if overall.queries:
        out.append(f"- heading-path hits among title-hits: "
                   f"{overall.heading_hits}/{sum(1 for r in rows if r.get('hit_rank'))}")
    out.append("")
    out.append("## Per category")
    out.append("")
    out.append("| category | n | hit@1 | hit@3 | hit@8 | MRR |")
    out.append("|---|---|---|---|---|---|")
    for name in sorted(summary.by_category):
        stats = summary.by_category[name]
        out.append(
            f"| {name} | {stats.queries} | {_fmt_pct(stats.hits_at_1, stats.queries)} "
            f"| {_fmt_pct(stats.hits_at_3, stats.queries)} "
            f"| {_fmt_pct(stats.hits_at_8, stats.queries)} | {stats.mrr:.3f} |"
        )
    out.append("")
    junk_removed = summary.junk_hits_top8_raw
    junk_total = summary.junk_slots_top8_raw
    out.append("## Junk-filter impact (raw top-8 slots)")
    out.append("")
    out.append(
        f"- junk-flagged results in raw top-8: {junk_removed}/{junk_total} slots "
        f"({_fmt_pct(junk_removed, junk_total)} of top-8 noise)"
    )
    out.append("")
    out.append("## Per query")
    out.append("")
    for row in rows:
        if row.get("error"):
            out.append(f"- {row['id']}: ERROR {row['error']}")
        else:
            rank = row.get("hit_rank")
            rank_text = str(rank) if rank is not None else "miss"
            out.append(f"- {row['id']} [{row['category']}]: rank {rank_text} "
                       f"-> {row.get('hit_title') or '-'}")
    out.append("")
    return "\n".join(out)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Run the lybrix retrieval eval dataset.")
    parser.add_argument("--dataset", required=True, help="Path to a JSONL dataset file")
    parser.add_argument("--top-k", type=int, default=8, help="search top_k (default 8)")
    parser.add_argument("--label", required=True, help="Label for the output files")
    parser.add_argument(
        "--junk-filter",
        choices=("on", "off"),
        default="on",
        help="Exclude TOC/junk-heading results from metrics (default on)",
    )
    parser.add_argument(
        "--collection",
        default=None,
        help="Collection id or name (resolved to id); omit for the full corpus",
    )
    args = parser.parse_args(argv)

    cases = load_dataset(args.dataset)
    dataset_by_id = {case.id: vars(case) | {"expected_headings": case.expected_headings} for case in cases}
    client = McpClient()
    collection_id = resolve_collection(client, args.collection) if args.collection else None

    rows: list[dict] = []
    for index, case in enumerate(cases):
        rows.append(run_query(client, case, args.top_k, collection_id))
        if index < len(cases) - 1:
            time.sleep(QUERY_PACING_S)

    junk_filter = filters_mod.is_junk if args.junk_filter == "on" else None
    summary = judge.evaluate(rows, dataset_by_id, junk_filter=junk_filter)

    timestamp = _dt.datetime.now(_dt.UTC).strftime("%Y%m%dT%H%M%SZ")
    RESULTS_DIR.mkdir(parents=True, exist_ok=True)
    json_path = RESULTS_DIR / f"{args.label}-{timestamp}.json"
    md_path = RESULTS_DIR / f"{args.label}-{timestamp}.md"
    json_path.write_text(
        json.dumps(
            {
                "label": args.label,
                "created_utc": timestamp,
                "dataset": str(args.dataset),
                "top_k": args.top_k,
                "junk_filter": args.junk_filter,
                "collection_id": collection_id,
                "rows": rows,
                "summary": {
                    "total_queries": summary.total_queries,
                    "evaluated": summary.evaluated,
                    "errors": summary.errors,
                    "empty_responses": summary.empty_responses,
                    "hit_at_1": summary.overall.hit_at_1,
                    "hit_at_3": summary.overall.hit_at_3,
                    # hit_at_top_k (R-19): the old f"hit_at_{args.top_k}" key
                    # carried the judge's fixed hit@8 value, undercounting for
                    # top_k > 8 and misnaming a correct value for top_k < 8.
                    "hit_at_top_k": summary.overall.hit_at_top_k,
                    "mrr": summary.overall.mrr,
                    "by_category": {
                        name: {
                            "queries": stats.queries,
                            "hit_at_1": stats.hit_at_1,
                            "hit_at_3": stats.hit_at_3,
                            "hit_at_8": stats.hit_at_8,
                            "mrr": stats.mrr,
                        }
                        for name, stats in summary.by_category.items()
                    },
                },
            },
            indent=2,
        ),
        encoding="utf-8",
    )
    md_path.write_text(
        render_summary_md(args.label, rows, summary, args.top_k), encoding="utf-8"
    )

    overall = summary.overall
    print(f"label={args.label} queries={summary.total_queries} "
          f"hit@1={overall.hit_at_1:.2f} hit@3={overall.hit_at_3:.2f} "
          f"hit@{args.top_k}={overall.hit_at_top_k:.2f} mrr={overall.mrr:.3f}")
    print(f"wrote {json_path}")
    print(f"wrote {md_path}")

    failures = [row for row in rows if row.get("error")]
    if failures:
        print(f"error: {len(failures)} query(ies) raised during the run:", file=sys.stderr)
        for row in failures:
            print(f"  {row['id']}: {row['error']}", file=sys.stderr)
        return 1
    rate = any_results_rate(rows)
    if rate < MIN_RESULT_FRACTION:
        print(
            f"error: only {rate:.0%} of queries returned any results "
            f"(< {MIN_RESULT_FRACTION:.0%}) — check the endpoint URL, auth token, "
            f"and --collection value (a wrong value silently filters to zero rows)",
            file=sys.stderr,
        )
        return 1
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except McpConfigError as exc:
        print(f"error: {exc}", file=sys.stderr)
        raise SystemExit(2) from exc
