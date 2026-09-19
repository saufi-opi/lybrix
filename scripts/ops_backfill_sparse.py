"""Backfill Qdrant points with client-side BM25 sparse vectors (PRD §12).

One-time op: streams chunks (chunk_hash → text) from Postgres in
deterministic chunk_hash order, encodes each chunk's text into a 21-bit
TF sparse vector (retrieval.bm25.encode_bm25 — the server's idf modifier
multiplies in IDF at query time), and update_vectors's the `bm25` sparse
vector in batches. update_vectors touches ONLY the named sparse vector —
the dense vector is never read or rewritten (which is why upsert is never
used here: it would rewrite the dense vector dict).

Resumable via a checkpoint file (resume-after-hash, mid-batch trim);
rate-limited; --dry-run counts without writing; --patch-idf idempotently
applies the server-side idf modifier.

CLI:
    python -m scripts.ops_backfill_sparse --dry-run          # counts only
    python -m scripts.ops_backfill_sparse --patch-idf        # idempotent idf modifier
    python -m scripts.ops_backfill_sparse --batch 512 --rate 300

Runs from VM1 over Tailscale (QDRANT_URL / QDRANT_API_KEY / DATABASE_URL
from the usual ~/lybrix/.env conventions) or inside the api container.
Checkpoint scripts/bm25_backfill_checkpoint.json (gitignored) is written
after every batch and makes interrupted runs resume strictly after the
last chunk_hash.
"""

from __future__ import annotations

import argparse
import datetime as _dt
import json
import sys
import time
from collections.abc import Iterator
from itertools import islice
from pathlib import Path

from core.config import get_settings
from core.db.models import Chunk
from qdrant_client import QdrantClient
from qdrant_client import models as qm
from retrieval.bm25 import encode_bm25
from retrieval.qdrant import COLLECTION_NAME, ensure_bm25_idf, point_id_for
from sqlalchemy import select

CHECKPOINT_PATH = Path(__file__).resolve().parent / "bm25_backfill_checkpoint.json"


def batched(iterable, n: int) -> Iterator[list]:
    """itertools.batched is 3.12+ — local 3.11 needs this shim (same shape)."""
    it = iter(iterable)
    while batch := tuple(islice(it, n)):
        yield list(batch)


def load_checkpoint(path: Path) -> str | None:
    """Last completed chunk_hash from a checkpoint file, or None."""
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return None
    return data.get("last_chunk_hash")


def write_checkpoint(path: Path, last_chunk_hash: str, done: int, total: int) -> None:
    path.write_text(
        json.dumps(
            {
                "last_chunk_hash": last_chunk_hash,
                "done": done,
                "total": total,
                "updated_at": _dt.datetime.now(_dt.UTC).isoformat(),
            },
            indent=2,
        ),
        encoding="utf-8",
    )


def run_backfill(
    client,
    session_factory,
    batch: int = 512,
    rate: float = 300.0,
    checkpoint_path: Path | None = None,
    dry_run: bool = False,
    total_hint: int | None = None,
) -> int:
    """Stream PG chunks ordered by chunk_hash; encode + update_vectors per batch.

    Resume: chunks with hash <= checkpoint's last_chunk_hash are skipped
    (deterministic ordering makes re-runs no-ops for finished prefix);
    a batch interrupted mid-way is trimmed to start strictly after that
    hash. checkpoint_path=None disables checkpointing (tests; main() always
    supplies one). Rate limit: sleep so the per-batch update_vectors cadence
    stays under `rate` calls/sec. Returns 0 on success, 1 on fatal error.
    """
    resume_after = load_checkpoint(checkpoint_path) if checkpoint_path else None
    if resume_after:
        print(f"resuming after chunk_hash {resume_after[:12]}…")

    batch_no = 0
    done = 0
    last_hash: str | None = None
    batch_start = time.monotonic()
    try:
        with session_factory() as session:
            stream = session.execute(
                select(Chunk.chunk_hash, Chunk.text).order_by(Chunk.chunk_hash)
            ).yield_per(batch)
            for batch_rows in batched(stream, batch):
                if resume_after and batch_rows[-1][0] <= resume_after:
                    done += len(batch_rows)
                    continue
                # trim a partially-finished batch on resume
                start_index = 0
                if resume_after:
                    while start_index < len(batch_rows) and batch_rows[start_index][0] <= resume_after:
                        start_index += 1
                    if start_index == len(batch_rows):
                        done += len(batch_rows)
                        continue
                    done += start_index
                if not dry_run:
                    # ONLY the 'bm25' named sparse vector — never dense,
                    # never upsert (upsert would rewrite the dense vector).
                    client.update_vectors(
                        collection_name=COLLECTION_NAME,
                        points=[
                            qm.PointVectors(
                                id=point_id_for(chunk_hash),
                                vector={"bm25": qm.SparseVector(**encode_bm25(text))},
                            )
                            for chunk_hash, text in batch_rows[start_index:]
                        ],
                        wait=False,
                    )
                done += len(batch_rows) - start_index
                last_hash = batch_rows[-1][0]
                batch_no += 1
                # pacing: keep the per-batch write cadence under `rate` calls/s
                elapsed = time.monotonic() - batch_start
                min_seconds = len(batch_rows) / rate
                if not dry_run and elapsed < min_seconds:
                    time.sleep(min_seconds - elapsed)
                batch_start = time.monotonic()
                print(
                    f"batch {batch_no}: {done}"
                    f"{f'/{total_hint}' if total_hint else ''} sparse vectors "
                    f"{'counted (dry-run)' if dry_run else 'written'}"
                )
                if not dry_run and checkpoint_path:
                    write_checkpoint(checkpoint_path, last_hash, done, total_hint or done)
    except Exception as exc:  # fatal: report and fail loudly
        print(f"error: backfill failed after {done} chunks: {exc!r}", file=sys.stderr)
        return 1

    if not dry_run:
        print("verifying: counting points carrying the bm25 sparse vector…")
        with_sparse = client.count(
            collection_name=COLLECTION_NAME,
            count_filter=qm.Filter(
                must=[qm.HasVectorCondition(has_vector="bm25")]
            ),
            exact=True,
        )
        total = client.count(collection_name=COLLECTION_NAME, exact=True)
        print(
            f"backfill complete: {done} sparse vectors written; "
            f"points with bm25 vector: {with_sparse.count}/{total.count}"
        )
        if last_hash is not None and checkpoint_path:
            write_checkpoint(checkpoint_path, last_hash, done, total_hint or done)
    else:
        print(
            f"dry-run: {done} chunks would be encoded + written to {COLLECTION_NAME} "
            f"(~{batch_no} batches at --batch {batch}; zero Qdrant writes performed)"
        )
    return 0


def main(argv: list[str] | None = None) -> int:
    ap = argparse.ArgumentParser(
        description="Backfill Qdrant points with client-side BM25 sparse vectors"
    )
    ap.add_argument("--dry-run", action="store_true", help="Counts only, zero Qdrant writes")
    ap.add_argument(
        "--patch-idf",
        action="store_true",
        help="Idempotently apply the idf modifier to the bm25 sparse space, then exit",
    )
    ap.add_argument("--batch", type=int, default=512, help="Points per update_vectors call")
    ap.add_argument("--rate", type=float, default=300.0, help="Max update_vectors calls/sec")
    ap.add_argument(
        "--checkpoint",
        type=Path,
        default=CHECKPOINT_PATH,
        help="Checkpoint file path (default: scripts/bm25_backfill_checkpoint.json)",
    )
    args = ap.parse_args(argv)

    s = get_settings()
    client = QdrantClient(url=s.qdrant_url, api_key=s.qdrant_api_key, timeout=60)

    if args.patch_idf:
        name = ensure_bm25_idf(client, s)
        print(f"bm25 idf modifier ensured on collection {name!r}")
        return 0

    from core.db.session import make_engine, make_session_factory

    factory = make_session_factory(make_engine(s))
    with factory() as session:
        total = len(session.execute(select(Chunk.chunk_hash)).all())
    return run_backfill(
        client,
        factory,
        batch=args.batch,
        rate=args.rate,
        checkpoint_path=args.checkpoint,
        dry_run=args.dry_run,
        total_hint=total,
    )


if __name__ == "__main__":
    sys.exit(main())
