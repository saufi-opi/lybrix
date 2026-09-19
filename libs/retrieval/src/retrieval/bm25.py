"""Client-side BM25 sparse encoding (Phase 1, revised design).

The production qdrant/qdrant:v1.19.1 image silently DROPS unknown
`functions` fields on collection create/PATCH and bundles no inference
runtime (document-inference BM25 fails with "InferenceService URL not
configured"), so server-side native BM25 is unavailable. The verified live
path is client-side sparse vectors: encode text here, write the `bm25`
sparse space via update_vectors, and query through the existing
Prefetch(query=SparseVector, using="bm25") path fused by native RRF.

With the collection's sparse config patched to modifier=idf (server-side
IDF at query time — see retrieval.qdrant.ensure_bm25_idf), the client only
needs term-frequency (TF) weights: value = 1.0 + log(tf) per unique token.

Encoding recipe (probed live):
- tokenize: lowercase, alphanumeric runs, min length 2
- index: FNV-1a hash per token mod 2^21 (21-bit index space)
- collisions are tolerated: two tokens hashing to the same index SUM their
  counts before the log transform (TF-accumulation into one dict slot).
"""

from __future__ import annotations

import math
import re

# 21-bit index space: 2M buckets keeps collision rate low at 30k+ vocab
# while staying well under Qdrant's sparse indexing comfort zone.
SPARSE_DIM = 2**21

_FNV_OFFSET = 0x811C9DC5
_FNV_PRIME = 0x01000193
_TOKEN_RE = re.compile(r"[a-z0-9]+")


def fnv1a(token: str) -> int:
    """32-bit FNV-1a hash of the UTF-8 bytes of `token`."""
    h = _FNV_OFFSET
    for byte in token.encode("utf-8"):
        h ^= byte
        h = (h * _FNV_PRIME) & 0xFFFFFFFF
    return h


def tokenize(text: str) -> list[str]:
    """Lowercase alphanumeric runs, min length 2."""
    return [t for t in _TOKEN_RE.findall(text.lower()) if len(t) >= 2]


def encode_bm25(text: str) -> dict:
    """Encode text into a sparse vector dict {"indices": [...], "values": [...]}.

    indices are sorted ascending; values are 1.0 + log(tf) per unique token
    index (TF only — the server multiplies in IDF via the idf modifier).
    Token-hash collisions sum their counts in the same index slot.
    """
    tf: dict[int, int] = {}
    for token in tokenize(text):
        index = fnv1a(token) % SPARSE_DIM
        tf[index] = tf.get(index, 0) + 1
    indices = sorted(tf)
    values = [1.0 + math.log(tf[index]) for index in indices]
    return {"indices": indices, "values": values}
