"""Rebuild Qdrant from Postgres chunks (PRD §12 scripts/reindex.py).

Qdrant is a cache, not a source of truth (§13.5): after a vector-store
loss, this regenerates every point from chunks + a TEI endpoint.
"""

from __future__ import annotations

import argparse
import sys

from qdrant_client import QdrantClient
from sqlalchemy import select

from core.config import get_settings
from core.db.models import Chunk, Document
from core.db.session import make_engine, make_session_factory
from embedding.client import TeiClient
from retrieval.qdrant import ensure_collection, upsert_chunks


def reindex(batch: int = 48) -> int:
    s = get_settings()
    factory = make_session_factory(make_engine(s))
    qdrant = QdrantClient(url=s.qdrant_url, api_key=s.qdrant_api_key, timeout=30)
    ensure_collection(qdrant, s)

    with factory() as session:
        chunks = session.execute(select(Chunk).order_by(Chunk.doc_id, Chunk.seq)).scalars().all()
        cols = {d.id: d.collection_id for d in session.execute(select(Document)).scalars()}
        print(f"reindexing {len(chunks)} chunks into Qdrant")

        done = 0
        with TeiClient(s.tei_ingest_url) as tei:
            for i in range(0, len(chunks), batch):
                group = chunks[i : i + batch]
                vectors = tei.embed([c.text for c in group])
                upsert_chunks(
                    qdrant,
                    [
                        {
                            "chunk_hash": c.chunk_hash,
                            "doc_id": c.doc_id,
                            "collection_id": cols.get(c.doc_id, ""),
                            "vector": vec,
                            "page_start": c.page_start,
                            "page_end": c.page_end,
                            "heading_path": c.heading_path,
                        }
                        for c, vec in zip(group, vectors)
                    ],
                    s,
                )
                done += len(group)
                print(f"  {done}/{len(chunks)}")
    return 0


def main() -> None:
    ap = argparse.ArgumentParser(description="Rebuild Qdrant points from Postgres chunks")
    ap.add_argument("--batch", type=int, default=48)
    args = ap.parse_args()
    sys.exit(reindex(args.batch))


if __name__ == "__main__":
    main()
