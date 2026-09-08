"""Re-embed a collection against a new model — no re-parsing (PRD G6,
§12 scripts/reembed.py).

Swapping the embedding model re-runs only embed+index: chunks and their
text stay in Postgres, so this script re-embeds every chunk and upserts
to Qdrant. The collection's embedding_model is updated in place after a
successful run.
"""

from __future__ import annotations

import argparse
import sys

from core.config import get_settings
from core.db.models import Chunk, Collection, Document
from core.db.session import make_engine, make_session_factory
from embedding.client import TeiClient
from qdrant_client import QdrantClient
from retrieval.qdrant import ensure_collection, upsert_chunks
from sqlalchemy import select


def reembed(collection_id: str, new_model: str, batch: int = 48) -> int:
    s = get_settings()
    factory = make_session_factory(make_engine(s))
    qdrant = QdrantClient(url=s.qdrant_url, api_key=s.qdrant_api_key, timeout=30)
    ensure_collection(qdrant, s)

    with factory() as session:
        col = session.get(Collection, collection_id)
        if col is None:
            print(f"collection {collection_id} not found")
            return 1
        old_model = col.embedding_model
        if old_model == new_model:
            print(f"collection already on {new_model}")
            return 0

        doc_ids = [
            d.id
            for d in session.execute(
                select(Document).where(Document.collection_id == collection_id)
            ).scalars()
        ]
        chunks = (
            session.execute(select(Chunk).where(Chunk.doc_id.in_(doc_ids)).order_by(Chunk.seq))
            .scalars()
            .all()
        )
        print(f"re-embedding {len(chunks)} chunks: {old_model} -> {new_model}")

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
                            "collection_id": collection_id,
                            "vector": vec,
                            "page_start": c.page_start,
                            "page_end": c.page_end,
                            "heading_path": c.heading_path,
                        }
                        for c, vec in zip(group, vectors, strict=True)
                    ],
                    s,
                )
                done += len(group)
                print(f"  {done}/{len(chunks)}")

        col.embedding_model = new_model
        session.commit()
    print("collection model updated")
    return 0


def main() -> None:
    ap = argparse.ArgumentParser(description="Migrate a collection to a new embedding model")
    ap.add_argument("collection")
    ap.add_argument("--model", required=True)
    ap.add_argument("--batch", type=int, default=48)
    args = ap.parse_args()
    sys.exit(reembed(args.collection, args.model, args.batch))


if __name__ == "__main__":
    main()
