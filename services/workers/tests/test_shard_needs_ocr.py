"""OCR-gate verdict persistence (shards.needs_ocr).

Covers:
  mark_shard_done persists needs_ocr True/False on the Shard row
  default stays False for legacy callers (keyword is optional)
  parser passes the live gate verdict through to mark_shard_done
"""

from __future__ import annotations

import uuid
from unittest.mock import MagicMock, patch

from core.db.models import Shard, ShardState
from core.db.repo import mark_shard_done
from workers.parser import handle_parse


class _StubSession:
    """Just enough Session for mark_shard_done: get() + flush()."""

    def __init__(self, shard: Shard):
        self._shard = shard

    def get(self, model, key):
        return self._shard

    def execute(self, stmt):  # shards_done bump — not under test here
        return MagicMock()

    def flush(self):
        pass


def _shard() -> Shard:
    return Shard(
        doc_id=uuid.uuid4(),
        idx=0,
        page_start=1,
        page_end=20,
        state=ShardState.PENDING,
    )


def _mark(shard: Shard, **kw) -> None:
    mark_shard_done(
        _StubSession(shard),
        shard.doc_id,
        0,
        duration_ms=1000,
        peak_rss_mb=100,
        parsed_uri="s3://parsed/x",
        **kw,
    )


def test_persists_needs_ocr_true():
    shard = _shard()
    _mark(shard, needs_ocr=True)
    assert shard.state == ShardState.DONE
    assert shard.needs_ocr is True


def test_persists_needs_ocr_false():
    shard = _shard()
    _mark(shard, needs_ocr=False)
    assert shard.needs_ocr is False


def test_default_false_for_legacy_callers():
    shard = _shard()
    _mark(shard)
    assert shard.needs_ocr is False


def _run_parse(need_ocr: bool):
    """Run handle_parse with mocked externals; return the mark_shard_done mock."""
    doc = MagicMock()
    doc.id = uuid.uuid4()
    verdict = MagicMock(needs_ocr=need_ocr, mean_chars_per_page=123.0)
    with (
        patch("workers.parser.repo.get_document", return_value=doc),
        patch("workers.parser.repo.claim_shard", return_value=MagicMock(attempts=1)),
        patch("workers.parser.repo.mark_shard_done") as done,
        patch("workers.parser.repo.book_settled", return_value=False),
        patch("workers.parser.s3.make_s3"),
        patch("workers.parser.s3.download_to"),
        patch("workers.parser.s3.upload_json"),
        patch("workers.parser.needs_ocr", return_value=verdict),
        patch("workers.parser.build_converter", return_value=MagicMock()),
    ):
        settings = MagicMock()
        settings.parser_pdf_cache_dir = None
        settings.parser_soft_rss_mb = 6144
        settings.s3_bucket_raw = "raw"

        with patch("workers.parser.tempfile.mkdtemp", return_value="/tmp/parse-x"):
            handle_parse(
                MagicMock(),
                {"doc_id": str(doc.id), "idx": 0, "page_start": 1, "page_end": 20},
                redis=MagicMock(),
                settings=settings,
            )
    return done


def test_parser_passes_gate_verdict_through():
    done = _run_parse(need_ocr=True)
    done.assert_called_once()
    assert done.call_args.kwargs["needs_ocr"] is True


def test_parser_passes_false_verdict_through():
    done = _run_parse(need_ocr=False)
    done.assert_called_once()
    assert done.call_args.kwargs["needs_ocr"] is False
