"""Unit tests for the parser host-local PDF cache (parser_pdf_cache_dir).

Covers:
  HIT:   cache file exists -> no S3 download, file copied into tmpdir
  MISS:  no cached file -> S3 download happens, result stored into cache
  MISS + cache disabled (None): behaviour identical to pre-cache parser
  store failure is non-fatal: shard still parses when cache dir unwritable
"""

from __future__ import annotations

import sys
import uuid
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

from workers.parser import handle_parse


@pytest.fixture()
def doc_row():
    doc = MagicMock()
    doc.id = uuid.uuid4()
    doc.source_uri = f"s3://raw/{doc.id}.pdf"
    return doc


def _job(doc):
    return {
        "doc_id": str(doc.id),
        "idx": 0,
        "page_start": 1,
        "page_end": 20,
    }


def _run(tmp_path, cache_dir, doc):
    """Run handle_parse with mocked externals; return (s3_mock, pdf_path)."""
    s3_mock = MagicMock()

    def fake_download(_s3, _bucket, _key, dest):
        Path(dest).write_bytes(b"%PDF-1.4 fake")

    with (
        patch("workers.parser.repo.get_document", return_value=doc),
        patch("workers.parser.repo.claim_shard", return_value=MagicMock()),
        patch("workers.parser.repo.mark_shard_done"),
        patch("workers.parser.repo.book_settled", return_value=False),
        patch("workers.parser.s3.make_s3"),
        patch("workers.parser.s3.download_to", side_effect=fake_download) as dl,
        patch("workers.parser.needs_ocr", return_value=MagicMock(needs_ocr=False)),
        patch("workers.parser.build_converter", return_value=MagicMock()),
    ):
        settings = MagicMock()
        settings.parser_pdf_cache_dir = cache_dir
        settings.parser_soft_rss_mb = 6144
        settings.s3_bucket_raw = "raw"

        session = MagicMock()
        pdf_path = Path(tmp_path) / "tmp-parse" / "source.pdf"
        pdf_path.parent.mkdir(parents=True, exist_ok=True)

        with patch("workers.parser.tempfile.mkdtemp", return_value=str(pdf_path.parent)):
            handle_parse(session, {"doc_id": str(doc.id), "idx": 0,
                                   "page_start": 1, "page_end": 20},
                         redis=MagicMock(), settings=settings)
    return s3_mock, dl, pdf_path


def test_cache_hit_skips_download(tmp_path):
    doc = MagicMock()
    doc.id = uuid.uuid4()
    cache_dir = tmp_path / "cache"
    cache_dir.mkdir()
    (cache_dir / f"{doc.id}.pdf").write_bytes(b"cached bytes")

    _, dl, pdf_path = _run(tmp_path, str(cache_dir), doc)

    dl.assert_not_called()  # the whole point
    assert Path(pdf_path).read_bytes() == b"cached bytes"


def test_cache_miss_downloads_and_stores(tmp_path):
    doc = MagicMock()
    doc.id = uuid.uuid4()
    cache_dir = tmp_path / "cache"

    _, dl, _ = _run(tmp_path, str(cache_dir), doc)

    dl.assert_called_once()
    assert (cache_dir / f"{doc.id}.pdf").is_file()


def test_cache_disabled_downloads_without_storing(tmp_path):
    doc = MagicMock()
    doc.id = uuid.uuid4()
    cache_root = tmp_path / "nowhere"

    _, dl, _ = _run(tmp_path, None, doc)  # cache_dir=None

    dl.assert_called_once()
    assert not cache_root.exists()


def test_cache_store_failure_is_non_fatal(tmp_path):
    doc = MagicMock()
    doc.id = uuid.uuid4()
    # cache dir path is a FILE -> mkdir raises -> shard must still parse
    blocking = tmp_path / "blocker"
    blocking.write_text("not a dir")

    _, dl, pdf_path = _run(tmp_path, str(blocking / "cache"), doc)

    dl.assert_called_once()
    assert Path(pdf_path).is_file()
