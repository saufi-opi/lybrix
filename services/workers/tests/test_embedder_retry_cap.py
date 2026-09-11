"""Tests for the embedder terminal-failure cap.

2026-09-11: a job whose upsert kept timing out was retried forever
(PEL/XAUTOCLAIM + janitor sweep), embed waste grew unbounded and the doc
never left state=parsing. Contract:

- handle_embed gains an optional ``redis`` param.
- Failures bump a per-doc retry counter in Redis; success resets it.
- When the counter reaches EMBED_MAX_ATTEMPTS, the embedder marks the
  doc FAILED (error_code DOC_EMBED_FAILED) and returns — the runner can
  ACK and stop retrying.
"""

from __future__ import annotations

import uuid
from unittest.mock import MagicMock, patch

import parsing.stitch  # noqa: F401  (must exist before patch targets resolve)
import pytest
from core.db.models import DocState
from core.errors import ErrorCode, PlatformError
from workers.embedder import EMBED_MAX_ATTEMPTS, handle_embed


def _doc(total=4, done=4, failed=0):
    doc = MagicMock()
    doc.id = uuid.uuid4()
    doc.collection_id = "coll"
    doc.total_shards = total
    doc.shards_failed = failed
    doc.state = "parsing"
    return doc


def _session():
    shard = MagicMock()
    shard.idx = 0
    session = MagicMock()
    session.execute.return_value.scalars.return_value.all.return_value = [shard]
    return session


def _patches(doc=None, **over):
    mapping = {
        "repo.get_document": MagicMock(return_value=doc or _doc()),
        "s3.make_s3": MagicMock(),
        "s3.parsed_key": MagicMock(return_value="k"),
        "TeiClient": MagicMock(),
        "upsert_chunks": MagicMock(),
        "ensure_collection": MagicMock(),
        "chunk_markdown": MagicMock(return_value=[]),
        "drop_duplicate_neighbours": MagicMock(return_value=[]),
    }
    mapping.update(over)
    target_patches = [patch(f"workers.embedder.{k}", v) for k, v in mapping.items()]
    # stitch is imported inside the function → patch at its own module path
    stitch_patches = [
        patch("parsing.stitch.load_shard_docs", MagicMock(return_value=[])),
        patch("parsing.stitch.stitch", MagicMock(return_value={"markdown": "x"})),
    ]
    return target_patches + stitch_patches


def test_failure_bumps_counter_and_raises_retryable():
    doc = _doc()
    session = _session()
    redis = MagicMock()
    fail_ctr = patch("workers.embedder._embed_fail", return_value=1)
    ps = _patches(doc=doc, upsert_chunks=MagicMock(side_effect=RuntimeError("timeout")))
    with fail_ctr as ctr:
        for p in ps:
            p.start()
        try:
            with pytest.raises(PlatformError) as ei:
                handle_embed(session, {"doc_id": str(doc.id)}, redis=redis)
            assert ei.value.code == ErrorCode.VECTOR_UPSERT_FAILED
            assert ei.value.retryable is True
        finally:
            for p in ps:
                p.stop()
    ctr.assert_called_once()


def test_success_resets_counter_and_sets_ready():
    doc = _doc()
    session = _session()
    redis = MagicMock()
    reset = patch("workers.embedder._embed_success_reset")
    ps = _patches(doc=doc)
    with reset as reset_mock:
        for p in ps:
            p.start()
        try:
            handle_embed(session, {"doc_id": str(doc.id)}, redis=redis)
        finally:
            for p in ps:
                p.stop()
    reset_mock.assert_called_once()


def test_failure_counter_has_expiry_window():
    """Counter must expire — isolated failures hours/days apart must not
    accumulate into a false cap (an outage should not permanently poison
    a doc)."""
    doc = _doc()
    session = _session()
    redis = MagicMock()
    ps = _patches(doc=doc, upsert_chunks=MagicMock(side_effect=RuntimeError("timeout")))
    for p in ps:
        p.start()
    try:
        with pytest.raises(PlatformError):
            handle_embed(session, {"doc_id": str(doc.id)}, redis=redis)
    finally:
        for p in ps:
            p.stop()
    redis.expire.assert_called_once()
    ttl = redis.expire.call_args[0][1]
    assert 3600 <= ttl <= 86400


def test_cap_reached_marks_doc_failed_and_stops():
    doc = _doc()
    session = _session()
    redis = MagicMock()
    fail_ctr = patch("workers.embedder._embed_fail", return_value=EMBED_MAX_ATTEMPTS)
    set_state = patch("workers.embedder.repo.set_doc_state")
    ps = _patches(doc=doc, upsert_chunks=MagicMock(side_effect=RuntimeError("timeout")))
    with fail_ctr, set_state as set_mock:
        for p in ps:
            p.start()
        try:
            handle_embed(session, {"doc_id": str(doc.id)}, redis=redis)
        finally:
            for p in ps:
                p.stop()
    set_mock.assert_called_once()
    args = set_mock.call_args
    # set_doc_state(session, doc_id, state, error_code=..., error_detail=...)
    assert args[0][2] == DocState.FAILED
    assert args.kwargs.get("error_code") == ErrorCode.DOC_EMBED_FAILED.value
