"""Stage 1 — Splitter worker (PRD §6.2).

Cheap, CPU-only, never touches Docling. Downloads the source once,
extracts the page count + outline, writes shard rows, and fans out one
doc.parse job per shard.
"""

from __future__ import annotations

import tempfile
from pathlib import Path

from core.config import get_settings
from core.db import repo
from core.db.models import DocState
from core.errors import ErrorCode, PlatformError
from core.queue import contracts, streams
from core.storage import s3
from parsing.splitter import chapter_aligned_bounds, extract_outline, fixed_bounds


def handle_split(session, job: dict, redis) -> None:
    settings = get_settings()

    doc_id = job["doc_id"]
    doc = repo.get_document(session, doc_id)
    if doc is None:
        raise PlatformError(ErrorCode.PDF_CORRUPT, f"document {doc_id} vanished")

    # Idempotency: re-delivered split jobs (janitor requeue / PEL reclaim)
    # must not re-insert shard rows. Shards PK is (doc_id, idx) — probe with
    # the composite key. If shards exist this doc is already split — just
    # advance UPLOADED → PARSING so the state machine converges.
    from core.db.models import Shard

    already = session.get(Shard, (doc_id, 0))
    if already is not None:
        if doc.state == DocState.UPLOADED:
            repo.set_doc_state(session, doc_id, DocState.PARSING)
        return

    local_path = Path(tempfile.mkdtemp(prefix="split-")) / "source.pdf"
    bucket = settings.s3_bucket_raw
    key = s3.raw_key(str(doc_id))
    try:
        s3.download_to(s3.make_s3(settings), bucket, key, str(local_path))
    except Exception as exc:  # pragma: no cover
        raise PlatformError(ErrorCode.PDF_CORRUPT, f"source missing: {exc}") from exc

    import pikepdf

    try:
        with pikepdf.open(str(local_path)) as pdf:
            n_pages = len(pdf.pages)
            outline = extract_outline(str(local_path))
    except pikepdf.PasswordError as exc:
        raise PlatformError(ErrorCode.PDF_ENCRYPTED, "source PDF is encrypted") from exc
    except Exception as exc:
        raise PlatformError(ErrorCode.PDF_CORRUPT, f"cannot open PDF: {exc}") from exc

    if outline:
        bounds = chapter_aligned_bounds(outline, n_pages, settings)
    else:
        bounds = fixed_bounds(n_pages, settings.shard_pages)

    repo.set_doc_state(session, doc_id, DocState.PARSING)
    session.flush()
    doc = repo.get_document(session, doc_id)
    doc.total_shards = len(bounds)
    doc.page_count = n_pages
    repo.insert_shards(session, doc_id, [(b.page_start, b.page_end) for b in bounds])
    session.flush()

    for b in bounds:
        streams.xadd_job(
            redis,
            streams.STREAM_PARSE,
            contracts.ParseJob(
                doc_id=doc_id,
                idx=b.idx,
                page_start=b.page_start,
                page_end=b.page_end,
                source_uri=doc.source_uri,
            ),
        )


def main() -> None:  # pragma: no cover - process entry
    import logging

    from core.db.session import make_engine, make_session_factory
    from core.observability.logging import configure_logging

    settings = get_settings()
    configure_logging(settings.log_level)
    logging.getLogger(__name__).info("splitter worker starting")

    factory = make_session_factory(make_engine(settings))
    redis = streams.make_redis(settings)

    def handler(session, job):
        handle_split(session, job, redis)

    from workers.runner import record_job_error, run_consumer

    run_consumer(
        stream=streams.STREAM_SPLIT,
        consumer=streams.new_consumer_name("splitter"),
        handler=handler,
        session_factory=factory,
        redis=redis,
        prefetch=settings.worker_prefetch,
        on_error=lambda s, j, e: record_job_error(s, j, e, stage="split"),
    )


if __name__ == "__main__":
    main()
