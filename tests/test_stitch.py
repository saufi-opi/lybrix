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
