"""Test doubles small enough to read at a glance."""

from __future__ import annotations

from types import SimpleNamespace


class FakePoint:
    """Stands in for a Qdrant ScoredPoint in hydrate() tests."""

    def __init__(self, pid: str, payload: dict, score: float):
        self.id = pid
        self.payload = payload
        self.score = score


class FakeSession:
    """Minimal session: preloaded chunk/document lookups for hydrate()."""

    def __init__(self, chunks=(), documents=()):
        self._by_hash = {c.chunk_hash: c for c in chunks}
        self._by_id = {d.id: d for d in documents}

    def execute(self, stmt, *a, **kw):
        # hydrate() issues select(Chunk).where(chunk_hash IN ...); we just
        # return every preloaded chunk — tests only need the mapping path.
        return SimpleNamespace(scalars=lambda: SimpleNamespace(all=lambda: list(self._by_hash.values())))

    def get(self, model, key):
        return self._by_id.get(key)
