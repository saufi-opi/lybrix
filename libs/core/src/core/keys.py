"""Single source of truth for API-key hashing (shared by api + mcp services)."""

from __future__ import annotations

import hashlib


def hash_key(raw: str) -> str:
    return hashlib.sha256(raw.encode()).hexdigest()
