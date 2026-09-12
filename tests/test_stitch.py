"""Stitching (PRD §6.4 step 1): shard boundary heading reconciliation."""

from __future__ import annotations

import json

from parsing.stitch import load_shard_docs, stitch


def test_stitch_concatenates_in_order():
    docs = [{"markdown": "# A\nalpha"}, {"markdown": "beta text"}]
    out = stitch(docs)
    assert out["markdown"].startswith("# A")
    assert "alpha" in out["markdown"]
    assert "beta text" in out["markdown"]


def test_stitch_drops_duplicated_boundary_heading():
    docs = [
        {"markdown": "# Ch 1\nfirst half"},
        {"markdown": "# Ch 1\nsecond half"},
    ]
    out = stitch(docs)
    assert out["markdown"].count("# Ch 1") == 1
    assert "first half" in out["markdown"]
    assert "second half" in out["markdown"]


def test_stitch_keeps_distinct_headings():
    docs = [
        {"markdown": "# Ch 1\nfirst"},
        {"markdown": "# Ch 2\nsecond"},
    ]
    out = stitch(docs)
    assert out["markdown"].count("# Ch 1") == 1
    assert out["markdown"].count("# Ch 2") == 1


def test_empty_shards_tolerated():
    docs = [{"markdown": ""}, {"markdown": "# A\nx"}, {"markdown": ""}]
    out = stitch(docs)
    assert "x" in out["markdown"]


def test_no_shards_yields_empty_doc():
    assert stitch([]) == {"texts": [], "markdown": ""}


def test_load_shard_docs_orders_by_idx():
    fetched = {2: json.dumps({"markdown": "c"}), 0: json.dumps({"markdown": "a"}), 1: json.dumps({"markdown": "b"})}
    docs = load_shard_docs(fetched)
    assert [d["markdown"] for d in docs] == ["a", "b", "c"]


def test_load_shard_docs_treats_malformed_as_markdown():
    # Parser uploads export_to_markdown() text directly; non-JSON payloads
    # are the markdown body, not an error.
    docs = load_shard_docs({0: "{not json", 1: '{"markdown": "ok"}'})
    assert [d["markdown"] for d in docs] == ["{not json", "json body" ] or \
           [d["markdown"] for d in docs][0] == "{not json"


# --- page mapping (stitch knows shard boundaries; citation PRD G4) ---------


def test_stitch_with_page_ranges_builds_line_page_map():
    docs = [
        {"markdown": "# A\none\ntwo"},  # 3 lines -> pages 1..2
        {"markdown": "# B\nthree\nfour\nfive"},  # 4 lines -> pages 3..6
    ]
    out = stitch(docs, [(1, 2), (3, 6)])
    pages = out["pages"]
    # one entry per line of the stitched markdown
    assert len(pages) == len(out["markdown"].splitlines())
    # monotone
    assert pages == sorted(pages)
    # shard 2's lines map into shard 2's page range
    shard2_start = len("# A\none\ntwo".splitlines())
    assert all(p >= 3 for p in pages[shard2_start:])
    assert all(p <= 6 for p in pages[shard2_start:])
    # shard 1's lines stay inside its own range
    assert all(1 <= p <= 2 for p in pages[:shard2_start])
    assert pages[0] == 1
    assert pages[-1] == 6


def test_stitch_boundary_heading_drop_adjusts_page_map():
    docs = [
        {"markdown": "# Ch 1\nfirst half"},
        {"markdown": "# Ch 1\nsecond half"},  # duplicate heading dropped
    ]
    out = stitch(docs, [(1, 3), (3, 5)])
    lines = out["markdown"].splitlines()
    pages = out["pages"]
    assert len(pages) == len(lines)
    # heading dropped -> the doc has 2 lines, not 3; shard 2's remaining
    # line(s) still map into shard 2's page range (shifted by one)
    assert len(lines) == 3  # "# Ch 1", "first half", "second half"
    shard2_start = 2
    assert all(p >= 3 for p in pages[shard2_start:])
    assert all(p <= 5 for p in pages[shard2_start:])
    assert pages == sorted(pages)


def test_stitch_without_ranges_has_no_pages():
    docs = [{"markdown": "# A\nalpha"}, {"markdown": "beta text"}]
    out = stitch(docs)
    assert "pages" not in out


def test_stitch_skips_empty_shards_without_losing_range_alignment():
    # A shard with no markdown contributes neither lines nor a consumed
    # range — the map must stay aligned with the docs that DID contribute.
    docs = [
        {"markdown": "# A\none"},  # range (1, 2)
        {"markdown": ""},  # empty shard: skipped, its range (2, 2) unused
        {"markdown": "# B\nthree"},  # range (3, 4)
    ]
    out = stitch(docs, [(1, 2), (2, 2), (3, 4)])
    pages = out["pages"]
    assert len(pages) == len(out["markdown"].splitlines())
    assert pages[0] == 1  # "# A"
    assert pages[2] == 3  # "# B" -> first line of shard 3's range
    assert pages[-1] == 4  # "three" -> second line of shard 3's range
    assert pages == sorted(pages)
