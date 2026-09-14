"""Stage 3 — Embedder worker (PRD §6.4).

Stitch shard JSONs → chunk the stitched doc → drop overlap duplicates →
insert chunks into Postgres → embed in moderate batches against
tei-ingest → upsert to Qdrant with chunk_hash as point id → set doc
ready/partial with completeness.

Terminal-failure cap (2026-09-11 incident): a job whose Qdrant upsert
kept timing out was retried forever — PEL/XAUTOCLAIM plus the janitor
sweep re-enqueued it endlessly, and the doc never left state=parsing.
Failures now bump a per-doc Redis counter; on reaching
EMBED_MAX_ATTEMPTS the doc is marked FAILED (terminal) and the handler
returns so the runner ACKs. Success resets the counter.
"""

from __future__ import annotations

import contextlib
import logging
import uuid
from concurrent.futures import ThreadPoolExecutor

from chunking import chunk_markdown, drop_duplicate_neighbours
from core.config import get_settings
from core.db import repo
from core.db.models import Chunk, DocState
from core.errors import ErrorCode, PlatformError
from core.queue import streams
from core.storage import s3
from embedding.client import TeiClient
from retrieval.qdrant import ensure_collection, upsert_chunks
from sqlalchemy import select
from sqlalchemy.orm import Session

logger = logging.getLogger(__name__)

EMBED_MAX_ATTEMPTS = 5

# Parallel S3 prefetch fan-out for shard JSONs (R-9). A book with N shards
# used to pay N sequential get_object round-trips before stitch/chunk/embed
# could start; the pool overlaps them. Module-level (not a Settings field)
# so no config plumbing this round.
PREFETCH_WORKERS = 8


def _prefetch_shards(s3c, doc_id: str, shard_rows, settings) -> dict[int, str]:
    """Fetch every shard's parsed JSON, mapping shard.idx -> utf-8 text.

    Semantics match the old serial loop exactly, just overlapped:

    - any per-shard failure propagates (no swallow, no in-pool retry) so
      the runner's on_error → embed retry/cap path behaves as before;
    - the boto3 client is shared across workers (thread-safe for
      get_object);
    - small books (<=1 shard) skip the executor entirely — a pool would
      cost more than the single round-trip it saves.
    """
    def _one(shard) -> tuple[int, str]:
        key = s3.parsed_key(doc_id, shard.idx)
        obj = s3c.get_object(Bucket=settings.s3_bucket_parsed, Key=key)
        return shard.idx, obj["Body"].read().decode("utf-8")

    if len(shard_rows) <= 1:
        return dict(_one(shard) for shard in shard_rows)

    with ThreadPoolExecutor(max_workers=PREFETCH_WORKERS) as pool:
        return dict(pool.map(_one, shard_rows))


def _embed_fail(redis, doc_id: str) -> int:
    """Bump + return the per-doc embed failure counter.

    The counter carries a 6h expiry: isolated failures hours apart must
    not accumulate into a false cap (an infra outage should not
    permanently poison a doc), while a tight pathological loop still
    reaches EMBED_MAX_ATTEMPTS within minutes.
    """
    try:
        n = int(redis.incr(f"embed:retries:{doc_id}") or 0)
        redis.expire(f"embed:retries:{doc_id}", 6 * 3600)
        return n
    except Exception:
        return 1  # fail-safe: treat as first failure


def _embed_success_reset(redis, doc_id: str) -> None:
    with contextlib.suppress(Exception):
        redis.delete(f"embed:retries:{doc_id}")


def handle_embed(session: Session, job: dict, redis=None) -> None:
    s = get_settings()
    doc_id = uuid.UUID(str(job["doc_id"]))
    doc = repo.get_document(session, doc_id)
    if doc is None:
        raise PlatformError(ErrorCode.PDF_CORRUPT, f"document {doc_id} vanished")

    from core.db.models import Shard

    shard_rows = (
        session.execute(
            select(Shard).where(Shard.doc_id == doc_id, Shard.state == "done").order_by(Shard.idx)
        )
        .scalars()
        .all()
    )
    if not shard_rows:
        raise PlatformError(ErrorCode.PDF_CORRUPT, "no parsed shards to embed")

    s3c = s3.make_s3(s)
    fetch = _prefetch_shards(s3c, str(doc_id), shard_rows, s)

    from parsing.stitch import load_shard_docs, stitch

    # 1-based inclusive shard page ranges, aligned with the idx-sorted shard
    # rows (the same order load_shard_docs emits). Failed shards have no
    # JSON so they are absent from both sides — ranges stay aligned.
    shard_page_ranges = [(sh.page_start, sh.page_end) for sh in shard_rows]

    try:
        stitched = stitch(load_shard_docs(fetch), shard_page_ranges)
        chunks = drop_duplicate_neighbours(
            chunk_markdown(
                stitched.get("markdown", ""), max_tokens=512, pages=stitched.get("pages")
            )
        )
    except Exception as exc:
        # Stitch/chunk failures are deterministic-ish — count them too so a
        # permanently-broken doc can reach the cap instead of looping.
        attempts = _bump_or_fail(session, redis, doc_id, exc)
        if attempts is None:
            return
        raise PlatformError(ErrorCode.PDF_CORRUPT, f"stitch/chunk failed: {exc}") from exc

    # Idempotent insert: UNIQUE(doc_id, chunk_hash) → ON CONFLICT DO NOTHING
    # (comments promised this; the bare add() actually raised UniqueViolation
    # on any re-delivered embed job — janitor requeue / PEL reclaim make
    # duplicate embed jobs normal, so the insert must be conflict-safe).
    from sqlalchemy.dialects.postgresql import insert as pg_insert

    for c in chunks:
        stmt = pg_insert(Chunk).values(
            doc_id=doc_id,
            chunk_hash=c.chunk_hash,
            seq=c.seq,
            text=c.text,
            token_count=c.token_count,
            page_start=c.page_start,
            page_end=c.page_end,
            heading_path=list(c.heading_path),
        ).on_conflict_do_nothing(index_elements=["doc_id", "chunk_hash"])
        session.execute(stmt)
    session.flush()

    # Embed against tei-ingest in moderate batches; TEI's dynamic batcher
    # packs them — our job is a steady stream of moderate requests (§6.4).
    from qdrant_client import QdrantClient

    qdrant = QdrantClient(url=s.qdrant_url, api_key=s.qdrant_api_key, timeout=10)
    ensure_collection(qdrant, s)
    points = []
    with TeiClient(
        s.tei_ingest_url,
        backend=s.embed_backend,
        model=s.embed_model,
        truncate_chars=s.embed_truncate_chars,
    ) as tei:
        batches = [chunks[i : i + s.embed_batch_size] for i in range(0, len(chunks), s.embed_batch_size)]
        for group in batches:
            vectors = tei.embed([c.text for c in group])
            for c, vec in zip(group, vectors, strict=True):
                points.append(
                    {
                        "chunk_hash": c.chunk_hash,
                        "doc_id": doc_id,
                        "collection_id": doc.collection_id or "",
                        "vector": vec,
                        "page_start": c.page_start,
                        "page_end": c.page_end,
                        "heading_path": list(c.heading_path),
                    }
                )
    try:
        upsert_chunks(qdrant, points, s)
    except Exception as exc:
        # Vector-write failure — transient most of the time (Qdrant restart,
        # net hiccup). Count it; after EMBED_MAX_ATTEMPTS the doc goes
        # FAILED and the retry loop ends (2026-09-11 timeout loop).
        attempts = _bump_or_fail(session, redis, doc_id, exc)
        if attempts is None:
            return
        raise PlatformError(
            ErrorCode.VECTOR_UPSERT_FAILED, f"qdrant upsert failed: {exc}"
        ) from exc

    failed = doc.shards_failed
    repo.set_doc_state(session, doc_id, DocState.PARTIAL if failed > 0 else DocState.READY)
    session.flush()
    doc = repo.get_document(session, doc_id)
    doc.chunk_count = len(chunks)
    if doc.total_shards:
        doc.completeness = round((doc.total_shards - failed) / doc.total_shards, 4)
    if redis is not None:
        _embed_success_reset(redis, str(doc_id))


def _bump_or_fail(session: Session, redis, doc_id, exc: Exception) -> int | None:
    """Count a failure; when the cap is reached mark the doc FAILED and
    return None (handler must return → runner ACKs → retry loop ends)."""
    if redis is None:
        return 0  # legacy callers: no counter, keep old raise behavior
    attempts = _embed_fail(redis, str(doc_id))
    if attempts < EMBED_MAX_ATTEMPTS:
        logger.warning("embed failed (attempt %d/%d) doc=%s: %s", attempts, EMBED_MAX_ATTEMPTS, doc_id, exc)
        return attempts
    logger.error("embed cap reached (%d attempts) doc=%s — marking FAILED", attempts, doc_id)
    repo.set_doc_state(
        session,
        doc_id,
        DocState.FAILED,
        error_code=ErrorCode.DOC_EMBED_FAILED.value,
        error_detail=f"embed failed {attempts}x: {exc}",
    )
    session.flush()
    return None


def main() -> None:  # pragma: no cover - process entry
    import logging

    from core.db.session import make_engine, make_session_factory
    from core.observability.logging import configure_logging

    settings = get_settings()
    configure_logging(settings.log_level)
    logging.getLogger(__name__).info("embedder worker starting")

    factory = make_session_factory(make_engine(settings))
    redis = streams.make_redis(settings)

    from workers.runner import record_job_error, run_consumer

    def handler(session, job):
        handle_embed(session, job, redis=redis)

    run_consumer(
        stream=streams.STREAM_EMBED,
        consumer=streams.new_consumer_name("embedder"),
        handler=handler,
        session_factory=factory,
        redis=redis,
        prefetch=settings.worker_prefetch,
        on_error=lambda s, j, e: record_job_error(s, j, e, stage="embed"),
    )


if __name__ == "__main__":
    main()
