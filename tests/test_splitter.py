"""Splitter bound arithmetic (PRD §6.2): a book is never a unit of work."""

from __future__ import annotations

import pytest
from parsing.splitter import chapter_aligned_bounds, fixed_bounds


def test_fixed_bounds_exact_division():
    bounds = fixed_bounds(40, 20, overlap=0)
    assert [(b.page_start, b.page_end) for b in bounds] == [(1, 20), (21, 40)]
    assert [b.idx for b in bounds] == [0, 1]


def test_fixed_bounds_remainder():
    bounds = fixed_bounds(45, 20, overlap=0)
    assert bounds[-1].page_end == 45
    assert bounds[-1].page_start == 41
    assert all(b.page_end >= b.page_start for b in bounds)


def test_fixed_bounds_overlap_never_repeats_a_page_twice_far():
    """With 1-page overlap, consecutive shards share exactly one page and
    coverage stays total."""
    bounds = fixed_bounds(100, 20, overlap=1)
    assert bounds[0].page_start == 1
    assert bounds[0].page_end == 20
    assert bounds[1].page_start == 20  # overlap page
    assert bounds[-1].page_end == 100
    # every page covered
    covered = set()
    for b in bounds:
        covered.update(range(b.page_start, b.page_end + 1))
    assert covered == set(range(1, 101))


def test_fixed_bounds_single_page_book():
    bounds = fixed_bounds(1, 20)
    assert [(b.page_start, b.page_end) for b in bounds] == [(1, 1)]


def test_fixed_bounds_rejects_zero_pages():
    with pytest.raises(ValueError):
        fixed_bounds(0, 20)


def test_chapter_aligned_snaps_to_bookmarks():
    outline = [(1, "Intro"), (21, "Ch 1"), (61, "Ch 2")]
    bounds = chapter_aligned_bounds(outline, 80, shard_pages=20)
    # a boundary must exist exactly at each chapter start
    starts = {b.page_start for b in bounds}
    assert {21, 61} <= starts
    # coverage complete
    covered = set()
    for b in bounds:
        covered.update(range(b.page_start, b.page_end + 1))
    assert covered == set(range(1, 81))


def test_chapter_aligned_no_shard_crosses_chapter_edge():
    outline = [(1, "A"), (10, "B")]
    bounds = chapter_aligned_bounds(outline, 25, shard_pages=20)
    for b in bounds:
        # shard either ends before chapter B starts, or starts at/after it
        assert b.page_end < 10 or b.page_start >= 10


def test_chapter_aligned_degenerate_outline_falls_back():
    outline = [(5, "x"), (5, "x")]  # duplicate/degenerate
    bounds = chapter_aligned_bounds(outline, 10, shard_pages=5)
    assert bounds and all(b.page_end >= b.page_start for b in bounds)
