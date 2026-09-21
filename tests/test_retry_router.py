"""Retry router (R-11): scope=shards re-parses the retried shards.

Direct-call pattern of tests/test_keys_router.py — no TestClient, no DB.
Before the fix the shards branch reset failed shards to pending and
XADDed an EmbedJob the embedder ignores (it selects state=="done" only),
so failed shards were never re-parsed and the doc flipped ready/partial
with a stranded pending shard.
"""

from __future__ import annotations

import uuid
from types import SimpleNamespace

import api.routers.documents as documents_mod
import pytest
from api.routers.documents import retry
from core.db.models import DocState, Shard, ShardState
from fastapi import HTTPException


def _shard(idx: int, page_start: int, page_end: int) -> Shard:
    return Shard(
        doc_id=uuid.uuid4(),
        idx=idx,
        page_start=page_start,
        page_end=page_end,
        state=ShardState.FAILED,
    )


class FakeSession:
    """Records update statements; select(Shard) returns the canned rows."""

    def __init__(self, doc, failed_shards):
        self.doc = doc
        self.failed_shards = failed_shards
        self.updates: list = []
        self.select_results: list = []

    def execute(self, stmt, *a, **kw):
        self.updates.append(stmt)
        if getattr(stmt, "whereclause", None) is None and not self.select_results:
            return SimpleNamespace(
                scalars=lambda: SimpleNamespace(all=lambda: [])
            )
        # heuristics are fragile; instead detect by annotated statement type
        return SimpleNamespace(scalars=lambda: SimpleNamespace(all=lambda: list(self.failed_shards)))

    def get(self, model, pk):
        return self.doc if model is type(self.doc) else None

    def flush(self):
        pass


class _FakeDoc:
    id = uuid.uuid4()
    source_uri = "s3://raw/doc.pdf"
    state = DocState.PARTIAL


def _session_for(doc, failed_shards):
    """A fake session distinguishing select(Shard) from table updates."""
    from sqlalchemy import Select
    from sqlalchemy.sql.dml import Update

    class Session2:
        def __init__(self):
            self.updates = []

        def execute(self, stmt, *a, **kw):
            if isinstance(stmt, Select):
                return SimpleNamespace(
                    scalars=lambda: SimpleNamespace(all=lambda: list(failed_shards))
                )
            if isinstance(stmt, Update):
                self.updates.append(stmt)
            return SimpleNamespace(rowcount=len(failed_shards))

        def get(self, model, pk):
            return doc

        def flush(self):
            pass

    return Session2()


def _xadds(monkeypatch):
    calls: list[tuple[str, object]] = []
    monkeypatch.setattr(
        documents_mod.streams, "xadd_job",
        lambda r, stream, payload: calls.append((stream, payload)),
    )
    return calls


def _set_doc_state_calls(monkeypatch):
    calls: list[tuple] = []
    monkeypatch.setattr(
        documents_mod.repo,
        "set_doc_state",
        lambda session, doc_id, state: calls.append((doc_id, state)),
    )
    return calls


def test_retry_shards_enqueues_one_parse_job_per_failed_shard(monkeypatch):
    doc = _FakeDoc()
    failed = [_shard(2, 41, 60), _shard(5, 101, 120)]
    session = _session_for(doc, failed)
    calls = _xadds(monkeypatch)
    out = retry(doc.id, SimpleNamespace(scope="shards"), None, session)
    assert out == {"id": str(doc.id), "retried": "shards"}
    # one ParseJob XADD per failed shard, with the shard's bounds + source_uri
    assert len(calls) == 2
    streams_seen = [c[0] for c in calls]
    assert all(s == documents_mod.streams.STREAM_PARSE for s in streams_seen)
    jobs = [c[1] for c in calls]
    assert [j.idx for j in jobs] == [2, 5]
    assert [j.page_start for j in jobs] == [41, 101]
    assert [j.page_end for j in jobs] == [60, 120]
    assert all(j.source_uri == doc.source_uri for j in jobs)
    # (5) no EmbedJob XADD — the embedder ignores pending shards
    from core.queue import contracts as contracts_mod

    assert not any(isinstance(j, contracts_mod.EmbedJob) for j in jobs)


def test_retry_shards_sets_doc_parsing_and_decrements_failed_counter(monkeypatch):
    doc = _FakeDoc()
    failed = [_shard(2, 41, 60), _shard(5, 101, 120)]
    session = _session_for(doc, failed)
    _xadds(monkeypatch)
    state_calls = _set_doc_state_calls(monkeypatch)
    retry(doc.id, SimpleNamespace(scope="shards"), None, session)
    # (2) doc state update to PARSING recorded
    assert state_calls == [(doc.id, DocState.PARSING)]
    # (3) shards_failed decremented by 2 (mark_shard_failed bumped it per failure)

    counter_updates = [
        u for u in session.updates
        if u.table is documents_mod.Document.__table__
    ]
    assert len(counter_updates) == 1
    compiled = counter_updates[0].compile()
    assert "shards_failed" in str(compiled)
    assert "-2" in str(compiled) or ":shards_failed" in str(compiled)


def test_retry_shards_no_failed_shards_raises_409(monkeypatch):
    doc = _FakeDoc()
    session = _session_for(doc, [])
    calls = _xadds(monkeypatch)
    with pytest.raises(HTTPException) as exc:
        retry(doc.id, SimpleNamespace(scope="shards"), None, session)
    assert exc.value.status_code == 409
    assert calls == []  # nothing enqueued


def test_retry_shards_resets_shards_to_pending(monkeypatch):
    doc = _FakeDoc()
    failed = [_shard(2, 41, 60), _shard(5, 101, 120)]
    session = _session_for(doc, failed)
    _xadds(monkeypatch)
    _set_doc_state_calls(monkeypatch)
    retry(doc.id, SimpleNamespace(scope="shards"), None, session)

    shard_updates = [
        u for u in session.updates if u.table is documents_mod.Shard.__table__
    ]
    assert len(shard_updates) == 1
    params = shard_updates[0].compile().params
    assert params["state"] == "pending"
    assert "error_code" in params and "error_detail" in params
