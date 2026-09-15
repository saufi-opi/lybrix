"""Tests for the parallel shard-JSON prefetch in the embedder (R-9).

handle_embed used to fetch each shard's parsed JSON from S3 one-by-one,
so a book with N shards paid N sequential round-trips before stitch/
chunk/embed could start. Contract:

- _prefetch_shards returns the same ``dict[int, str]`` (idx -> utf-8
  text) the serial loop produced;
- any per-shard failure propagates (no swallow, no in-pool retry) so
  the runner's on_error → embed retry/cap path behaves as before;
- small books (<=1 shard) never touch the executor.
"""

from __future__ import annotations

import threading
import uuid
from unittest.mock import MagicMock, patch

import parsing.stitch  # noqa: F401  (must exist before patch targets resolve)
from workers.embedder import _prefetch_shards


def _shards(n):
    rows = []
    for idx in range(n):
        row = MagicMock()
        row.idx = idx
        rows.append(row)
    return rows


def _s3c(bodies, *, sleeps=None):
    """Mock boto3 client whose get_object returns per-idx bodies (bytes)."""
    s3c = MagicMock()
    lock = threading.Lock()
    calls = []

    def get_object(Bucket, Key):
        idx = int(Key.rsplit("/", 1)[1].split(".")[0])
        with lock:
            calls.append(idx)
        if sleeps and idx in sleeps:
            threading.Event().wait(sleeps[idx])
        body = MagicMock()
        body.read.return_value = bodies[idx]
        return {"Body": body}

    s3c.get_object.side_effect = get_object
    s3c.calls = calls
    return s3c


def test_prefetches_all_shards_with_correct_text():
    n = 6
    bodies = {i: f'{{"text": "shard-{i}"}}'.encode() for i in range(n)}
    s3c = _s3c(bodies)
    fetch = _prefetch_shards(s3c, "doc-1", _shards(n), MagicMock())
    assert s3c.get_object.call_count == n
    assert set(fetch) == set(range(n))
    for i in range(n):
        assert fetch[i] == f'{{"text": "shard-{i}"}}'


def test_out_of_order_completion_matches_serial_semantics():
    """Shard 1 is slow; the pool must overlap it — every idx still maps to
    its own text (no cross-shard mixups when completions interleave)."""
    n = 4
    bodies = {i: f"body-{i}".encode() for i in range(n)}
    s3c = _s3c(bodies, sleeps={1: 0.15})
    fetch = _prefetch_shards(s3c, "doc-1", _shards(n), MagicMock())
    assert fetch == {i: f"body-{i}" for i in range(n)}


def test_single_shard_skips_executor(monkeypatch):
    """The <=1-shard path must not create a thread pool at all — patch the
    executor to explode if instantiated."""
    import workers.embedder as emb

    def _boom(*a, **k):
        raise AssertionError("ThreadPoolExecutor must not be used for <=1 shard")

    monkeypatch.setattr(emb, "ThreadPoolExecutor", _boom)
    s3c = _s3c({0: b"only"})
    fetch = _prefetch_shards(s3c, "doc-1", _shards(1), MagicMock())
    assert fetch == {0: "only"}


def test_failure_on_one_shard_propagates():
    n = 3
    bodies = {i: f"body-{i}".encode() for i in range(n)}
    s3c = _s3c(bodies)

    real = s3c.get_object.side_effect

    def get_object(Bucket, Key):
        if Key.endswith("/1.md"):
            raise RuntimeError("s3 explode")
        return real(Bucket=Bucket, Key=Key)

    s3c.get_object.side_effect = get_object
    try:
        _prefetch_shards(s3c, "doc-1", _shards(n), MagicMock())
    except RuntimeError as exc:
        assert "s3 explode" in str(exc)
    else:
        raise AssertionError("expected the per-shard failure to propagate")


def test_bytes_decoded_as_utf8():
    payload = '{"text": "héllo — ünïcode"}'.encode()
    s3c = _s3c({0: payload, 1: b"plain"})
    fetch = _prefetch_shards(s3c, "doc-1", _shards(2), MagicMock())
    assert all(isinstance(v, str) for v in fetch.values())
    assert fetch[0] == '{"text": "héllo — ünïcode"}'
    assert fetch[1] == "plain"


def test_handle_embed_uses_prefetch_helper():
    """handle_embed must route its fetch through _prefetch_shards on the
    same code path (no behavior flag) — patch it and confirm it's the
    source of the fetch dict handed to stitch."""
    doc = MagicMock()
    doc.id = uuid.uuid4()
    doc.collection_id = "coll"
    doc.total_shards = 2
    doc.shards_failed = 0
    doc.state = "parsing"

    rows = _shards(2)
    session = MagicMock()
    session.execute.return_value.scalars.return_value.all.return_value = rows

    tei = MagicMock()
    tei.embed.return_value = []
    tei.__enter__.return_value = tei
    with (
        patch("workers.embedder.repo.get_document", return_value=doc),
        patch("workers.embedder.repo.set_doc_state"),
        patch("workers.embedder.s3.make_s3"),
        patch("workers.embedder._prefetch_shards", return_value={0: "a", 1: "b"}) as pf,
        patch("parsing.stitch.load_shard_docs", MagicMock(return_value=[])) as lsd,
        patch("parsing.stitch.stitch", MagicMock(return_value={"markdown": "x"})),
        patch("workers.embedder.chunk_markdown", MagicMock(return_value=[])),
        patch("workers.embedder.drop_duplicate_neighbours", MagicMock(side_effect=lambda cs: cs)),
        patch("workers.embedder.TeiClient", MagicMock(return_value=tei)),
        patch("workers.embedder.upsert_chunks"),
        patch("workers.embedder.ensure_collection"),
        patch("qdrant_client.QdrantClient"),
    ):
        from workers.embedder import handle_embed

        handle_embed(session, {"doc_id": str(doc.id)}, redis=MagicMock())
    pf.assert_called_once()
    lsd.assert_called_once_with({0: "a", 1: "b"})
