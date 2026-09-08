"""tests — unit suite. DB-free by design: Postgres-touching paths are
exercised in staging (docker compose up), mirroring books-rag's
no-live-services-in-CI stance."""

from tests.helpers import FakePoint, FakeSession  # re-export convenience

__all__ = ["FakePoint", "FakeSession"]
