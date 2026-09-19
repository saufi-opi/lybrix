"""Compare two eval results files: aggregate deltas + per-query regression list.

    uv run python -m scripts.eval.compare scripts/eval/results/baseline-*.json \
                                                scripts/eval/results/phase1-*.json

Matches queries by `id`; reports hit@1/3/8 and MRR deltas and lists per-query
regressions (rank got worse or a hit was lost) plus improvements. Exit 0
either way — it is a measurement tool, not a gate — but --fail-on-regression
gives CI a gate later.
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path


def load_rows(path: str) -> dict[str, dict]:
    """{id -> row} from a run.py results JSON; errors/empty rows included."""
    data = json.loads(Path(path).read_text(encoding="utf-8"))
    return {row["id"]: row for row in data["rows"]}


def hit_rank(row: dict) -> int | None:
    """Effective hit rank: None for error/miss rows; misses rank worse than
    any real rank (top_k + 1) so deltas and regressions stay well-defined."""
    if row.get("error") or not row.get("results"):
        return None
    return row.get("hit_rank")


def classify(base_row: dict, cand_row: dict, top_k: int) -> str:
    """Compare one query pair: 'regression', 'improvement', or 'unchanged'."""
    base_rank, cand_rank = hit_rank(base_row), hit_rank(cand_row)
    if base_rank == cand_rank:
        return "unchanged"
    base_key = base_rank if base_rank is not None else top_k + 1
    cand_key = cand_rank if cand_rank is not None else top_k + 1
    return "regression" if cand_key > base_key else "improvement"


def compare(base_path: str, cand_path: str, top_k: int) -> dict:
    """Deltas + per-query regression/improvement lists, matched by id."""
    base, cand = json.loads(Path(base_path).read_text(encoding="utf-8")), json.loads(
        Path(cand_path).read_text(encoding="utf-8")
    )
    base_rows, cand_rows = load_rows(base_path), load_rows(cand_path)
    regressions, improvements = [], []
    shared = sorted(set(base_rows) & set(cand_rows))
    for query_id in shared:
        verdict = classify(base_rows[query_id], cand_rows[query_id], top_k)
        if verdict == "regression":
            regressions.append(query_id)
        elif verdict == "improvement":
            improvements.append(query_id)
    base_summary, cand_summary = base.get("summary", {}), cand.get("summary", {})
    deltas = {}
    for metric in ("hit_at_1", "hit_at_3", "hit_at_8", "mrr"):
        if metric in base_summary and metric in cand_summary:
            deltas[metric] = round(cand_summary[metric] - base_summary[metric], 4)
    return {
        "matched": len(shared),
        "only_in_base": sorted(set(base_rows) - set(cand_rows)),
        "only_in_candidate": sorted(set(cand_rows) - set(base_rows)),
        "deltas": deltas,
        "regressions": regressions,
        "improvements": improvements,
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Compare two eval results files (A vs B)."
    )
    parser.add_argument("base", help="Baseline results JSON")
    parser.add_argument("candidate", help="Candidate results JSON")
    parser.add_argument(
        "--fail-on-regression",
        action="store_true",
        help="Exit non-zero when any query regressed (for CI gating later)",
    )
    args = parser.parse_args(argv)
    top_k = json.loads(Path(args.base).read_text(encoding="utf-8")).get("top_k", 8)
    report = compare(args.base, args.candidate, top_k)

    print(f"matched queries: {report['matched']}")
    if report["only_in_base"]:
        print(f"only in base: {', '.join(report['only_in_base'])}")
    if report["only_in_candidate"]:
        print(f"only in candidate: {', '.join(report['only_in_candidate'])}")
    for metric, delta in report["deltas"].items():
        print(f"{metric}: {delta:+.4f}")
    for title, ids in (("regressions", report["regressions"]),
                       ("improvements", report["improvements"])):
        print(f"{title}: {len(ids)}")
        for query_id in ids:
            print(f"  - {query_id}")
    if args.fail_on_regression and report["regressions"]:
        print(f"error: {len(report['regressions'])} regression(s)", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
