"""Bulk ingest a directory of PDFs via presign → PUT → commit (PRD §12).

Usage:
    python -m scripts.backfill ./books --collection engineering-handbooks

Dedupe is content-based (sha256); already-committed duplicates are
skipped with a notice, never an error.
"""

from __future__ import annotations

import argparse
import hashlib
import sys
from pathlib import Path

import httpx


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def backfill(directory: Path, collection: str, api_url: str, api_key: str) -> int:
    headers = {"Authorization": f"Bearer {api_key}"}
    pdfs = sorted(directory.glob("*.pdf"))
    if not pdfs:
        print(f"no PDFs in {directory}")
        return 1
    committed = skipped = failed = 0
    with httpx.Client(timeout=600, headers=headers) as client:
        for pdf in pdfs:
            digest = sha256_file(pdf)
            presign = client.post(
                f"{api_url}/v1/documents/presign",
                json={"collection_id": collection, "byte_size": pdf.stat().st_size},
            )
            if presign.status_code != 200:
                print(f"{pdf.name}: presign failed {presign.status_code}")
                failed += 1
                continue
            doc_id = presign.json()["doc_id"]
            upload_url = presign.json()["upload_url"]

            put = client.put(upload_url, content=pdf.read_bytes())
            if put.status_code >= 400:
                print(f"{pdf.name}: PUT failed {put.status_code}")
                failed += 1
                continue

            commit = client.post(
                f"{api_url}/v1/documents/{doc_id}/commit",
                json={
                    "collection_id": collection,
                    "content_sha256": digest,
                    "title": pdf.stem,
                },
            )
            if commit.status_code == 409:
                print(f"{pdf.name}: duplicate, skipped")
                skipped += 1
            elif commit.status_code == 202:
                print(f"{pdf.name}: committed as {doc_id}")
                committed += 1
            else:
                print(f"{pdf.name}: commit failed {commit.status_code} {commit.text[:120]}")
                failed += 1
    print(f"done: {committed} committed, {skipped} duplicates, {failed} failed")
    return 1 if failed else 0


def main() -> None:
    ap = argparse.ArgumentParser(description="Bulk-ingest a directory of PDFs")
    ap.add_argument("directory", type=Path)
    ap.add_argument("--collection", required=True)
    ap.add_argument("--api-url", default="http://localhost:8000")
    ap.add_argument("--api-key", default="")
    args = ap.parse_args()
    sys.exit(backfill(args.directory, args.collection, args.api_url, args.api_key))


if __name__ == "__main__":
    main()
