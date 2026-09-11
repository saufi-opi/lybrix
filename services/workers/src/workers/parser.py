"""Stage 2 — Parser worker: THE heavy one (PRD §6.3).

Memory discipline, all five controls required:
  one job per process (prefetch=1, concurrency=1)   — compose env
  process recycling (PARSER_RECYCLE_AFTER)          — compose + this module
  soft RSS budget (SoftOOM)                         — parsing.memory
  hard container limit (mem_limit: 8g)              — compose backstop
  tmpfs cap (2g)                                    — compose

Retry ladder (attempts are not identical):
  1 as configured
  2 re-split this shard into 4 sub-shards (TODO M2)
  3 single-page shards (TODO M2)
  4 disable table structure, text-only (TODO M2)
  final mark shard failed, emit event, continue the book
"""

from __future__ import annotations

import logging
import os
import shutil
import tempfile
import time
import uuid
from pathlib import Path

from core.config import Settings, get_settings
from core.db import repo
from core.errors import ErrorCode, PlatformError
from core.queue import contracts, streams
from core.storage import s3
from parsing.converter import build_converter
from parsing.memory import check_rss_budget
from parsing.ocr_gate import needs_ocr


def handle_parse(session, job: dict, redis, settings: Settings | None = None) -> None:
    s = settings or get_settings()
    doc_id = uuid.UUID(str(job["doc_id"]))
    idx = int(job["idx"])
    page_start = int(job["page_start"])
    page_end = int(job["page_end"])

    doc = repo.get_document(session, doc_id)
    if doc is None:
        raise PlatformError(ErrorCode.PDF_CORRUPT, f"document {doc_id} vanished")

    worker_id = f"parser-{uuid.uuid4().hex[:8]}"
    shard = repo.claim_shard(session, doc_id, idx, worker_id, s)
    session.flush()
    if shard is None:
        return  # someone else got it (§6.3 step 1)

    tmpdir = Path(tempfile.mkdtemp(prefix="parse-"))
    pdf_path = tmpdir / "source.pdf"
    cache_dir = s.parser_pdf_cache_dir
    cache_path = Path(cache_dir) / f"{doc_id}.pdf" if cache_dir else None
    if cache_path is not None and cache_path.is_file():
        # Cache hit — PDFs are immutable per doc_id, no download needed.
        # Copy (not hardlink/ln): docling may write sidecar files next to the
        # source, and tmpfs unlinking on recycle must never touch the cache.
        shutil.copyfile(cache_path, pdf_path)
        logging.getLogger(__name__).info(
            "pdf cache HIT %s (%.1f MB saved download)",
            doc_id,
            cache_path.stat().st_size / 1e6,
        )
    else:
        s3.download_to(
            s3.make_s3(s), s.s3_bucket_raw, s3.raw_key(str(doc_id)), str(pdf_path)
        )
        if cache_path is not None:
            # Atomic write into the host cache: tmp file in the same dir,
            # then rename. Concurrent shard jobs of the same book may race —
            # os.replace is atomic, losers just overwrite with identical bytes.
            try:
                cache_path.parent.mkdir(parents=True, exist_ok=True)
                tmp_cache = cache_path.with_suffix(".tmp")
                shutil.copyfile(pdf_path, tmp_cache)
                os.replace(tmp_cache, cache_path)
                logging.getLogger(__name__).info(
                    "pdf cache MISS -> STORED %s", doc_id
                )
            except OSError as cache_exc:
                # Cache is a pure optimization — never fail the shard over it.
                logging.getLogger(__name__).warning(
                    "pdf cache store failed (ignored): %s", cache_exc
                )

    started = time.monotonic()
    verdict = needs_ocr(str(pdf_path), page_start, page_end, s)
    logging.getLogger(__name__).info(
        "ocr gate %s shard=%d pages=%d-%d mean_chars/page=%.0f needs_ocr=%s",
        doc_id,
        idx,
        page_start,
        page_end,
        verdict.mean_chars_per_page,
        verdict.needs_ocr,
    )
    converter = build_converter(need_ocr=verdict.needs_ocr, settings=s)

    result = converter.convert(
        str(pdf_path),
        # docling 2.126: page_range is 1-based INCLUSIVE — (start, end) both
        # ≥1. Passing 0-based (page_start-1, ...) fails validation "start
        # must be ≥ 1" on every first shard. Overlap semantics unchanged
        # (page_end is inclusive both ways).
        page_range=(page_start, page_end),
    )
    check_rss_budget(s)
    markdown = result.document.export_to_markdown()

    parsed_key = s3.parsed_key(str(doc_id), idx)
    s3.upload_json(s3.make_s3(s), s.s3_bucket_parsed, parsed_key, markdown)

    duration_ms = int((time.monotonic() - started) * 1000)
    repo.mark_shard_done(
        session,
        doc_id,
        idx,
        duration_ms=duration_ms,
        peak_rss_mb=check_rss_budget(s),
        parsed_uri=f"s3://{s.s3_bucket_parsed}/{parsed_key}",
        needs_ocr=verdict.needs_ocr,
    )

    # last shard settled → enqueue embed (§6.3 step 7).
    # mark_shard_done() increments shards_done via SQL expression; refresh
    # the ORM row before checking, otherwise the final shard appears missing.
    session.flush()
    session.expire(doc)
    doc = repo.get_document(session, doc_id)
    if repo.book_settled(doc):
        streams.xadd_job(redis, streams.STREAM_EMBED, contracts.EmbedJob(doc_id=doc_id))


def main() -> None:  # pragma: no cover - process entry
    import logging

    from core.db.session import make_engine, make_session_factory
    from core.observability.logging import configure_logging

    settings = get_settings()
    configure_logging(settings.log_level)
    logging.getLogger(__name__).info("parser worker starting")

    factory = make_session_factory(make_engine(settings))
    redis = streams.make_redis(settings)

    def handler(session, job):
        handle_parse(session, job, redis, settings)

    from workers.runner import record_job_error, run_consumer

    run_consumer(
        stream=streams.STREAM_PARSE,
        consumer=streams.new_consumer_name("parser"),
        handler=handler,
        session_factory=factory,
        redis=redis,
        prefetch=1,  # §6.3: one job per process, non-negotiable
        on_error=lambda s, j, e: record_job_error(s, j, e, stage="parse"),
    )


if __name__ == "__main__":
    main()
