"""Tests for the eval harness logic: judge (normalization/matching/hit@k/MRR),
filters, dataset loader, runner predicates, and compare mode. All pure
functions on plain dicts — no network, no host env reads.
"""

from __future__ import annotations

import json

import pytest

from scripts.eval import compare as compare_mod
from scripts.eval import judge
from scripts.eval.dataset import DatasetError, load_dataset
from scripts.eval.filters import is_junk
from scripts.eval.run import MIN_RESULT_FRACTION, any_results_rate, resolve_collection

# --- title normalization (pinned cases from the plan) ----------------------


def test_normalize_cplusplus_adjacent():
    """'+' folds in place with NO space: C++ -> cplusplus. Normative —
    'c plus plus' would break the pinned cpluspluscrashcourse case."""
    assert judge.normalize_title("C++") == "cplusplus"
    assert judge.normalize_title("C++ Crash Course") == "cplusplus crash course"


def test_titles_match_slug_to_title():
    assert judge.titles_match(
        "cpluspluscrashcourse", "C++ Crash Course: A Fast-Paced Introduction"
    )


def test_titles_match_go_guard():
    """'Go' (2 space-stripped chars) must NOT match every title containing
    'go' — the >=8-char guard."""
    assert not judge.titles_match("Go", "Cloud Native Go")
    assert not judge.titles_match("Go", "Go in Action")
    assert not judge.titles_match("Go", "Golang for Pros")


def test_titles_match_exact_short_still_works():
    """Exact-equal space-stripped forms match even under the guard."""
    assert judge.titles_match("Go in Action", "Go in Action")
    assert judge.titles_match("Go", "Go")


def test_titles_match_long_go_titles_substring():
    assert judge.titles_match("Cloud Native Go", "Cloud Native Go")
    assert judge.titles_match("Get Programming with Go", "Get Programming with Go")


def test_titles_match_cxx_programming_language():
    assert judge.titles_match("The C++ Programming Language", "The C++ Programming Language")


def test_titles_match_sharp_folding():
    assert judge.titles_match("Code Like a Pro in C#", "Code Like a Pro in C#")


def test_author_as_title_normalizes_cleanly():
    assert judge.normalize_title("Albert P. Malvino, Jerald A. Brown") == (
        "albert p malvino jerald a brown"
    )


def test_space_stripped_len():
    assert judge.space_stripped_len("Go") == 2
    assert judge.space_stripped_len("Go in Action") == 10
    assert judge.space_stripped_len("C++ Crash Course") == 20


# --- title_hit / heading_hit ----------------------------------------------


def _result(title, heading_path=None, score=0.5):
    return {"doc_title": title, "heading_path": heading_path or [], "score": score}


def test_title_hit_returns_first_matching_rank():
    results = [_result("Think Java"), _result("Go in Action"), _result("Cloud Native Go")]
    assert judge.title_hit(["Go in Action"], results) == 2


def test_title_hit_none_when_no_match():
    assert judge.title_hit(["Missing Book"], [_result("Think Java")]) is None


def test_title_hit_skips_null_titles():
    results = [{"doc_title": None, "heading_path": [], "score": 0.5}, _result("Go in Action")]
    assert judge.title_hit(["Go in Action"], results) == 2


def test_heading_hit_substring_case_insensitive():
    result = _result("Go in Action", ["Chapter 2", "Select Statement"])
    assert judge.heading_hit(["select statement"], result)
    assert not judge.heading_hit(["Chapter 9"], result)


def test_heading_hit_false_without_expectations():
    assert not judge.heading_hit([], _result("T", ["anything"]))


# --- evaluate ---------------------------------------------------------------


def _case(case_id, category, titles, headings=None):
    return {"id": case_id, "expected_doc_titles": titles, "expected_headings": headings or []}


def test_evaluate_hit_rates_and_mrr():
    rows = [
        {"id": "a", "category": "exact", "results": [_result("T1")], "raw_results": []},
        {"id": "b", "category": "exact", "results": [_result("X"), _result("T2")],
         "raw_results": []},
        {"id": "c", "category": "drift", "results": [], "raw_results": []},
    ]
    dataset = {
        "a": _case("a", "exact", ["T1"]),
        "b": _case("b", "exact", ["T2"]),
        "c": _case("c", "drift", ["T3"]),
    }
    summary = judge.evaluate(rows, dataset)
    assert summary.evaluated == 2
    assert summary.empty_responses == 1
    assert summary.overall.hits_at_1 == 1
    assert summary.overall.hits_at_3 == 2
    assert summary.overall.hits_at_8 == 2
    assert summary.overall.mrr == pytest.approx((1.0 + 0.5) / 2)
    assert summary.by_category["exact"].queries == 2
    assert summary.by_category["drift"].queries == 1


def test_evaluate_error_rows_tracked_not_crashing():
    rows = [{"id": "a", "category": "exact", "error": "boom"}]
    summary = judge.evaluate(rows, {"a": _case("a", "exact", ["T"])})
    assert summary.errors == 1
    assert summary.evaluated == 0


def test_evaluate_heading_hit_rate_among_title_hits():
    rows = [
        {"id": "a", "category": "exact",
         "results": [_result("T1", ["Ch 1", "RRF"])], "raw_results": []},
    ]
    summary = judge.evaluate(rows, {"a": _case("a", "exact", ["T1"], ["RRF"])})
    assert summary.overall.heading_hits == 1
    summary = judge.evaluate(rows, {"a": _case("a", "exact", ["T1"], ["Absent"])})
    assert summary.overall.heading_hits == 0


# --- hit_at_top_k (R-19) ------------------------------------------------------


def test_hit_at_top_k_any_rank():
    """hit_at_top_k = len(reciprocal_ranks)/queries — correct for any top_k:
    3 queries, 2 hits (ranks 2 and 5) -> 2/3, while the fixed hit@1 counter
    stays 0 (neither hit was rank 1)."""
    stats = judge.CategoryStats()
    judge._accumulate(stats, 2, False)
    judge._accumulate(stats, 5, False)
    judge._accumulate(stats, None, False)
    assert stats.queries == 3
    assert stats.hit_at_top_k == pytest.approx(2 / 3)
    assert stats.hit_at_8 == pytest.approx(2 / 3)
    assert stats.hit_at_1 == 0.0
    assert stats.hit_at_3 == pytest.approx(1 / 3)


def test_hit_at_top_k_counts_rank12_for_top16_not_hit8():
    """A top_k=16-shaped case where rank 12 counts for top_k but not for
    the fixed hit@8 counters (the old f"hit_at_{top_k}" key carried hit@8
    and undercounted)."""
    stats = judge.CategoryStats()
    judge._accumulate(stats, 12, False)
    judge._accumulate(stats, None, False)
    assert stats.hit_at_top_k == pytest.approx(0.5)
    assert stats.hit_at_8 == 0.0


def test_hit_at_top_k_zero_queries():
    assert judge.CategoryStats().hit_at_top_k == 0.0


# --- junk filter ------------------------------------------------------------
def test_junk_toc_heading():
    assert is_junk(_result("Any Book", ["Front Matter", "Table of Contents"]))


def test_junk_bare_headings():
    for bare in ("Index", "See Also", "Problem", "Solution", "INDEX", "problem"):
        assert is_junk(_result("T", ["Some Chapter", bare])), bare


def test_junk_real_headings_pass():
    assert not is_junk(_result("Go in Action", ["Chapter 2", "Select Statement"]))
    assert not is_junk(_result("T", ["Index of Terms Revisited"]))  # exact last-entry only


def test_junk_front_matter_filenames():
    assert is_junk({"doc_title": "03-FM-index.indd", "heading_path": []})
    assert is_junk({"doc_title": "12-FM-Title", "heading_path": []})
    assert is_junk({"doc_title": "scan.dvi", "heading_path": []})
    assert is_junk({"doc_title": "doc.pdf", "heading_path": []})
    assert not is_junk({"doc_title": "Go in Action", "heading_path": []})


def test_junk_filter_impact_recorded_in_summary():
    junk = _result("03-FM-index.indd", [])
    rows = [{"id": "a", "category": "exact", "results": [_result("T1")],
             "raw_results": [junk, _result("T1")]}]
    summary = judge.evaluate(rows, {"a": _case("a", "exact", ["T1"])}, junk_filter=is_junk)
    assert summary.junk_hits_top8_raw == 1
    assert summary.junk_slots_top8_raw == 2


# --- dataset loader ---------------------------------------------------------


def test_loader_validates_and_parses(tmp_path):
    path = tmp_path / "ds.jsonl"
    path.write_text(
        json.dumps({"id": "x-1", "query": "q", "expected_doc_titles": ["Go in Action"],
                    "category": "exact", "notes": "n"}) + "\n" +
        json.dumps({"id": "x-2", "query": "q2", "expected_doc_titles": ["Pandas Brain Teasers"],
                    "expected_headings": ["Merge"], "category": "drift"}) + "\n"
    )
    cases = load_dataset(path)
    assert [c.id for c in cases] == ["x-1", "x-2"]
    assert cases[1].expected_headings == ["Merge"]


def test_loader_rejects_duplicate_id(tmp_path):
    path = tmp_path / "ds.jsonl"
    row = json.dumps({"id": "d", "query": "q", "expected_doc_titles": ["Go in Action"],
                      "category": "exact"})
    path.write_text(row + "\n" + row + "\n")
    with pytest.raises(DatasetError, match="duplicate id 'd'"):
        load_dataset(path)


def test_loader_rejects_unknown_category(tmp_path):
    path = tmp_path / "ds.jsonl"
    path.write_text(json.dumps({"id": "d", "query": "q",
                                "expected_doc_titles": ["Go in Action"],
                                "category": "weird"}) + "\n")
    with pytest.raises(DatasetError, match="unknown category"):
        load_dataset(path)


def test_loader_rejects_empty_expected_titles(tmp_path):
    path = tmp_path / "ds.jsonl"
    path.write_text(json.dumps({"id": "d", "query": "q", "expected_doc_titles": [],
                                "category": "exact"}) + "\n")
    with pytest.raises(DatasetError, match="line 1"):
        load_dataset(path)


def test_loader_rejects_short_unmatchable_title(tmp_path):
    """Seed contract: every expected title must normalize to >= 8 space-stripped
    chars — shorter titles can never match under the guard."""
    path = tmp_path / "ds.jsonl"
    path.write_text(json.dumps({"id": "d", "query": "q", "expected_doc_titles": ["Go"],
                                "category": "exact"}) + "\n")
    with pytest.raises(DatasetError, match="fewer than 8"):
        load_dataset(path)


def test_loader_line_numbers_in_errors(tmp_path):
    path = tmp_path / "ds.jsonl"
    good = json.dumps({"id": "ok", "query": "q", "expected_doc_titles": ["Go in Action"],
                       "category": "exact"})
    path.write_text(good + "\n" + "{not json\n")
    with pytest.raises(DatasetError, match="line 2"):
        load_dataset(path)


# --- runner predicates -------------------------------------------------------


def test_any_results_rate_and_threshold():
    rows = [
        {"results": [_result("T")]},
        {"results": []},
        {"results": [_result("T")]},
        {"error": "boom"},
    ]
    rate = any_results_rate(rows)
    assert rate == pytest.approx(2 / 3)
    assert rate >= MIN_RESULT_FRACTION


def test_any_results_rate_all_errors_is_zero():
    assert any_results_rate([{"error": "x"}]) == 0.0


class FakeCollectionsClient:
    def __init__(self, collections):
        self._collections = collections

    def list_collections(self):
        return self._collections


def test_resolve_collection_valid_id_sent_as_is():
    client = FakeCollectionsClient([{"id": "abc-123", "name": "909-corpus"}])
    assert resolve_collection(client, "abc-123") == "abc-123"


def test_resolve_collection_name_resolved_to_id():
    client = FakeCollectionsClient([{"id": "abc-123", "name": "909-corpus"}])
    assert resolve_collection(client, "909-corpus") == "abc-123"


def test_resolve_collection_invalid_errors_listing_valid():
    client = FakeCollectionsClient([{"id": "abc-123", "name": "909-corpus"}])
    with pytest.raises(SystemExit, match="abc-123"):
        resolve_collection(client, "nope")


# --- compare mode -------------------------------------------------------------


def _result_file(path, label, rows, top_k=8, summary=None):
    path.write_text(json.dumps({"label": label, "top_k": top_k, "rows": rows,
                                "summary": summary or {"hit_at_1": 0.5, "hit_at_3": 0.5,
                                                       "hit_at_8": 0.8, "mrr": 0.6}}))


def test_compare_deltas_and_regressions(tmp_path):
    base_rows = [
        {"id": "q1", "results": [_result("T1")], "hit_rank": 1},
        {"id": "q2", "results": [_result("X"), _result("T2")], "hit_rank": 2},
        {"id": "q3", "results": [_result("T3")], "hit_rank": 1},
    ]
    cand_rows = [
        {"id": "q1", "results": [_result("X"), _result("T1")], "hit_rank": 2},
        {"id": "q2", "results": [_result("X")], "hit_rank": None},
        {"id": "q3", "results": [_result("T3")], "hit_rank": 1},
        {"id": "q4", "results": [_result("T4")], "hit_rank": 1},
    ]
    base, cand = tmp_path / "base.json", tmp_path / "cand.json"
    _result_file(base, "baseline", base_rows, summary={"hit_at_1": 0.5, "hit_at_3": 0.5,
                                                       "hit_at_8": 0.8, "mrr": 0.6})
    _result_file(cand, "phase1", cand_rows, summary={"hit_at_1": 0.25, "hit_at_3": 0.25,
                                                     "hit_at_8": 0.75, "mrr": 0.4})
    report = compare_mod.compare(str(base), str(cand), top_k=8)
    assert report["matched"] == 3
    assert report["only_in_candidate"] == ["q4"]
    assert report["deltas"]["mrr"] == pytest.approx(-0.2)
    assert report["regressions"] == ["q1", "q2"]
    assert report["improvements"] == []


def test_compare_improvements_listed(tmp_path):
    base, cand = tmp_path / "base.json", tmp_path / "cand.json"
    _result_file(base, "b", [{"id": "q1", "results": [], "hit_rank": None}])
    _result_file(cand, "c", [{"id": "q1", "results": [_result("T")], "hit_rank": 3}])
    report = compare_mod.compare(str(base), str(cand), top_k=8)
    assert report["improvements"] == ["q1"]
    assert report["regressions"] == []


def test_compare_miss_to_miss_unchanged(tmp_path):
    base, cand = tmp_path / "base.json", tmp_path / "cand.json"
    _result_file(base, "b", [{"id": "q1", "results": [], "hit_rank": None}])
    _result_file(cand, "c", [{"id": "q1", "results": [], "hit_rank": None}])
    report = compare_mod.compare(str(base), str(cand), top_k=8)
    assert report["regressions"] == [] and report["improvements"] == []


def test_compare_fail_on_regression(tmp_path, capsys):
    base, cand = tmp_path / "base.json", tmp_path / "cand.json"
    _result_file(base, "b", [{"id": "q1", "results": [_result("T")], "hit_rank": 1}])
    _result_file(cand, "c", [{"id": "q1", "results": [], "hit_rank": None}])
    exit_code = compare_mod.main([str(base), str(cand), "--fail-on-regression"])
    assert exit_code == 1
    assert "1 regression(s)" in capsys.readouterr().err


def test_compare_exit_zero_without_flag(tmp_path):
    base, cand = tmp_path / "base.json", tmp_path / "cand.json"
    _result_file(base, "b", [{"id": "q1", "results": [_result("T")], "hit_rank": 1}])
    _result_file(cand, "c", [{"id": "q1", "results": [], "hit_rank": None}])
    assert compare_mod.main([str(base), str(cand)]) == 0
