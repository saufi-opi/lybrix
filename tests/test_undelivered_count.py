"""Tests for group-lag reading with NULL fallback.

Redis returns ``lag: None`` when the group's bookkeeping can no longer be
trusted — specifically after XDEL/XTRIM removes entries the counters
already included (2026-09-12: mass XDEL of duplicate embed jobs left
doc.embed lag=NULL and the pipeline dashboard showed waiting=1 for a
queue with 72 undelivered jobs).

Contract for ``undelivered_count``:

- lag is an int  → return it (fast path, O(1)).
- lag is None/missing → fall back to a live XRANGE page-scan after the
  group's last-delivered-id (exclusive) and return the exact count.
- no group on the stream → 0 (nothing is waiting).
- Redis errors → raise (callers decide policy; silently returning 0
  hides anomalies).
"""

from __future__ import annotations

import json

import pytest
from core.queue.streams import undelivered_count


class FakeRedis:
    """Minimal XRANGE/XINFO surface with real Redis cursor semantics."""

    def __init__(self, groups, entries=None, xrange_raises=None):
        self._groups = groups
        self._entries = entries or []  # [(id, fields)] sorted by id
        self._xrange_raises = xrange_raises
        self.calls = []

    def xinfo_groups(self, stream):
        return self._groups

    def xrange(self, stream, min="-", max="+", count=500):
        if self._xrange_raises is not None:
            raise self._xrange_raises
        self.calls.append(min)
        if min == "-":
            lo = ""
            exclusive = False
        elif min.startswith("("):
            lo = min[1:]
            exclusive = True
        else:
            lo = min
            exclusive = False
        out = []
        for eid, fields in self._entries:
            e_ms, e_seq = (int(p) for p in eid.split("-"))
            if lo:
                l_ms, l_seq = (int(p) for p in lo.split("-"))
                if (exclusive and (e_ms, e_seq) <= (l_ms, l_seq)) or (
                    not exclusive and (e_ms, e_seq) < (l_ms, l_seq)
                ):
                    continue
            out.append((eid, fields))
            if len(out) >= count:
                break
        return out


def test_int_lag_returned_directly():
    r = FakeRedis([{"name": "rag-workers", "last-delivered-id": "1-0", "lag": 42}])
    assert undelivered_count(r, "doc.embed") == 42
    assert r.calls == []  # fast path: no scan


def test_null_lag_falls_back_to_scan():
    r = FakeRedis(
        [{"name": "rag-workers", "last-delivered-id": "100-0", "lag": None}],
        entries=[
            ("100-0", {"job": json.dumps({"doc_id": "a"})}),  # delivered → excluded
            ("101-0", {"job": json.dumps({"doc_id": "b"})}),
            ("102-0", {"job": json.dumps({"doc_id": "c"})}),
        ],
    )
    assert undelivered_count(r, "doc.embed") == 2
    assert r.calls[0] == "(100-0"  # exclusive scan after cursor


def test_null_lag_paginates_large_backlog():
    entries = [(f"{i}-0", {"job": json.dumps({"doc_id": str(i)})}) for i in range(1, 1201)]
    r = FakeRedis(
        [{"name": "rag-workers", "last-delivered-id": "0-0", "lag": None}],
        entries=entries,
    )
    assert undelivered_count(r, "doc.embed") == 1200


def test_missing_group_means_zero():
    r = FakeRedis([])
    assert undelivered_count(r, "doc.embed") == 0


def test_redis_error_propagates():
    r = FakeRedis([{"name": "rag-workers", "last-delivered-id": "1-0", "lag": None}],
                  xrange_raises=RuntimeError("redis down"))
    with pytest.raises(RuntimeError):
        undelivered_count(r, "doc.embed")


def test_cursor_pagination_uses_exclusive_starts():
    entries = [(f"{i}-0", {"job": json.dumps({"doc_id": "x"})}) for i in range(1, 501)]
    entries += [(f"500-{i}", {"job": json.dumps({"doc_id": "y"})}) for i in range(1, 4)]
    r = FakeRedis(
        [{"name": "rag-workers", "last-delivered-id": "0-0", "lag": None}],
        entries=entries,
    )
    undelivered_count(r, "doc.embed")
    # second page starts strictly after the last id of page one
    assert r.calls[1] == "(500-0"
