"""Commit router (R-15/R-21): one streaming pass at commit verifies the
client hash, checks the %PDF- magic, and enforces the PRD §11 page cap.

Direct-call pattern of tests/test_keys_router.py — fakes only, no
TestClient, no DB, no MinIO.
"""

from __future__ import annotations

import uuid
from types import SimpleNamespace

import api.routers.documents as documents_mod
import pytest
from api.routers.documents import commit
from core.db.models import DocState, Document
from fastapi import HTTPException


class FakeS3Body:
    """Mirrors the real streaming contract: read(5) consumes the head;
    iter_chunks continues AFTER it (no double-count)."""

    def __init__(self, data: bytes):
        self._data = data
        self._pos = 0

    def read(self, n=-1):
        if n < 0:
            out = self._data[self._pos :]
            self._pos = len(self._data)
            return out
        out = self._data[self._pos : self._pos + n]
        self._pos += len(out)
        return out

    def iter_chunks(self, chunk_size=8):
        while self._pos < len(self._data):
            yield self._data[self._pos : self._pos + chunk_size]
            self._pos += chunk_size


def _sha(b: bytes) -> str:
    import hashlib

    return hashlib.sha256(b).hexdigest()


class FakeS3:
    def __init__(self, data: bytes = b"", error: Exception | None = None):
        self.data = data
        self.error = error
        self.gets: list = []

    def get_object(self, Bucket, Key):
        self.gets.append((Bucket, Key))
        if self.error is not None:
            raise self.error
        return {"Body": FakeS3Body(self.data)}


class FakeSession:
    def __init__(self):
        self.added: list = []
        self.flushed = 0

    def add(self, obj):
        self.added.append(obj)

    def flush(self):
        self.flushed += 1

    def get(self, model, pk):
        return None

    def execute(self, stmt, *a, **kw):
        # find_duplicate: no duplicates in these tests
        return SimpleNamespace(scalar_one_or_none=lambda: None)


def _pdf_bytes() -> bytes:
    return b"%PDF-1.4 fake pdf payload" + b"x" * 64


def _body(sha: str) -> SimpleNamespace:
    return SimpleNamespace(
        collection_id="c1",
        title="T",
        author=None,
        content_sha256=sha,
        metadata=None,
    )


def _request(doc_id=None, sha=None, data=None):
    data = data if data is not None else _pdf_bytes()
    doc_id = doc_id or uuid.uuid4()
    sha = sha or _sha(data)
    s3 = FakeS3(data)
    session = FakeSession()
    return doc_id, _body(sha), s3, session


def _patch_s3_and_redis(monkeypatch, s3: FakeS3, xadds: list):
    monkeypatch.setattr(documents_mod.s3, "make_s3", lambda *a, **kw: s3)
    monkeypatch.setattr(
        documents_mod.streams, "xadd_job",
        lambda r, stream, payload: xadds.append((stream, payload)),
    )
    monkeypatch.setattr(documents_mod.streams, "queue_depth", lambda r, stream: 0)
    monkeypatch.setattr(documents_mod, "_redis", lambda: None)
    # commit calls get_settings twice (backlog cap + s3 bucket + pages);
    # keep defaults — max_document_pages defaults to 800.


def _patch_pdf_pages(monkeypatch, pages: int = 5):
    """PdfDocument probe -> a sized fake (the canned bytes are not a real
    PDF; only the probe's page count matters to the router)."""
    import pypdfium2 as pdfium

    class FakePdf:
        def __init__(self, *a, **kw):
            pass

        def __len__(self):
            return pages

    monkeypatch.setattr(pdfium, "PdfDocument", FakePdf)


def test_commit_matching_hash_proceeds(monkeypatch):
    doc_id, body, s3, session = _request()
    xadds: list = []
    _patch_s3_and_redis(monkeypatch, s3, xadds)
    _patch_pdf_pages(monkeypatch)
    out = commit(doc_id, body, SimpleNamespace(name="tester"), session)
    assert out["state"] == DocState.UPLOADED.value
    # SplitJob XADDed
    assert len(xadds) == 1
    from core.queue import contracts as contracts_mod

    assert isinstance(xadds[0][1], contracts_mod.SplitJob)
    # one Document row stored with the (verified) hash
    rows = [o for o in session.added if isinstance(o, Document)]
    assert len(rows) == 1
    assert rows[0].content_sha256 == body.content_sha256


def test_commit_hash_mismatch_400_no_row_no_xadd(monkeypatch):
    doc_id, body, s3, session = _request(sha="0" * 64)
    xadds: list = []
    _patch_s3_and_redis(monkeypatch, s3, xadds)
    _patch_pdf_pages(monkeypatch)
    with pytest.raises(HTTPException) as exc:
        commit(doc_id, body, SimpleNamespace(name="tester"), session)
    assert exc.value.status_code == 400
    assert exc.value.detail == "content_sha256 mismatch"
    assert xadds == []
    assert not [o for o in session.added if isinstance(o, Document)]


def test_commit_wrong_magic_400(monkeypatch):
    data = b"NOTPDF" + b"y" * 64
    doc_id, body, s3, session = _request(data=data)
    xadds: list = []
    _patch_s3_and_redis(monkeypatch, s3, xadds)
    _patch_pdf_pages(monkeypatch)
    with pytest.raises(HTTPException) as exc:
        commit(doc_id, body, SimpleNamespace(name="tester"), session)
    assert exc.value.status_code == 400
    assert exc.value.detail == "uploaded object is not a PDF"
    assert xadds == []


def test_commit_pages_over_cap_400(monkeypatch):
    data = _pdf_bytes()
    doc_id, body, s3, session = _request(data=data)
    xadds: list = []
    _patch_s3_and_redis(monkeypatch, s3, xadds)
    _patch_pdf_pages(monkeypatch)

    # shrink the cap: get_settings() is lru_cache'd, so patch its result
    settings = documents_mod.get_settings()
    monkeypatch.setattr(
        documents_mod, "get_settings", lambda: settings.model_copy(update={"max_document_pages": 2})
    )
    with pytest.raises(HTTPException) as exc:
        commit(doc_id, body, SimpleNamespace(name="tester"), session)
    assert exc.value.status_code == 400
    assert "pages over cap" in exc.value.detail
    assert xadds == []


def test_commit_minio_client_error_400(monkeypatch):
    from botocore.exceptions import ClientError

    data = _pdf_bytes()
    doc_id, body, s3, session = _request(data=data)
    s3.error = ClientError({"Error": {"Code": "NoSuchKey"}}, "GetObject")
    xadds: list = []
    _patch_s3_and_redis(monkeypatch, s3, xadds)
    _patch_pdf_pages(monkeypatch)
    with pytest.raises(HTTPException) as exc:
        commit(doc_id, body, SimpleNamespace(name="tester"), session)
    assert exc.value.status_code == 400
    assert exc.value.detail == "object not uploaded"
    assert xadds == []


def test_commit_unreadable_pdf_400(monkeypatch):
    data = _pdf_bytes()
    doc_id, body, s3, session = _request(data=data)
    xadds: list = []
    _patch_s3_and_redis(monkeypatch, s3, xadds)
    _patch_pdf_pages(monkeypatch)

    import pypdfium2 as pdfium

    class Boom:
        def __init__(self, *a, **kw):
            raise RuntimeError("bad xref")

    monkeypatch.setattr(pdfium, "PdfDocument", Boom)
    with pytest.raises(HTTPException) as exc:
        commit(doc_id, body, SimpleNamespace(name="tester"), session)
    assert exc.value.status_code == 400
    assert "unreadable PDF" in exc.value.detail
    assert xadds == []
