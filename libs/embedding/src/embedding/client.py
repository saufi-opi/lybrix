"""TEI HTTP client: batching, exponential backoff, circuit breaker
(PRD §6.4 client resilience).

If TEI is down, the embed job retries later — it must not fail the book
and throw away the parse work already paid for. Hence: jittered
exponential backoff on 429/503, and a circuit breaker after 5
consecutive failures that raises TeiUnavailable (a retryable
EMBED_UNAVAILABLE) instead of hammering a dead endpoint.
"""

from __future__ import annotations

import random
import time

import httpx
from core.errors import ErrorCode, PlatformError


class TeiUnavailable(PlatformError):
    def __init__(self, detail: str) -> None:
        super().__init__(code=ErrorCode.EMBED_UNAVAILABLE, detail=detail)


class CircuitOpen(TeiUnavailable):
    def __init__(self, failures: int) -> None:
        super().__init__(f"circuit open after {failures} consecutive failures")


class TeiClient:
    def __init__(
        self,
        base_url: str,
        timeout_s: float = 60.0,
        max_retries: int = 5,
        breaker_threshold: int = 5,
    ) -> None:
        self._base_url = base_url.rstrip("/")
        self._client = httpx.Client(timeout=timeout_s)
        self._max_retries = max_retries
        self._breaker_threshold = breaker_threshold
        self._consecutive_failures = 0

    # -- circuit breaker ---------------------------------------------------
    def _record_success(self) -> None:
        self._consecutive_failures = 0

    def _record_failure(self) -> None:
        self._consecutive_failures += 1
        if self._consecutive_failures >= self._breaker_threshold:
            raise CircuitOpen(self._consecutive_failures)

    # -- public API ---------------------------------------------------------
    def embed(self, texts: list[str]) -> list[list[float]]:
        """POST /embed with jittered exponential backoff on 429/503.

        Transport errors and 429/503 retry; any other 4xx is a caller bug
        and fails fast without retry; the breaker aborts mid-ladder.
        """
        if self._consecutive_failures >= self._breaker_threshold:
            raise CircuitOpen(self._consecutive_failures)
        if not texts:
            return []

        delay = 0.5
        last_exc: Exception | None = None
        for attempt in range(self._max_retries + 1):
            try:
                resp = self._client.post(
                    f"{self._base_url}/embed", json={"inputs": texts}
                )
            except httpx.TransportError as exc:
                self._record_failure()
                last_exc = TeiUnavailable(f"transport error: {exc}")
            else:
                if resp.status_code in (429, 503):
                    # _record_failure raises CircuitOpen once the threshold
                    # is crossed — that aborts the ladder immediately.
                    self._record_failure()
                    last_exc = TeiUnavailable(f"TEI returned {resp.status_code}")
                elif resp.status_code >= 400:
                    # caller bug: fail fast, no retry
                    raise TeiUnavailable(
                        f"TEI returned {resp.status_code}: {resp.text[:200]}"
                    )
                else:
                    self._record_success()
                    return resp.json()

            if attempt < self._max_retries:
                time.sleep(delay + random.uniform(0, delay / 2))
                delay = min(delay * 2, 30.0)

        raise last_exc or TeiUnavailable("TEI embed failed")

    def health(self) -> bool:
        try:
            resp = self._client.get(f"{self._base_url}/health")
            return resp.status_code == 200
        except httpx.TransportError:
            return False

    def close(self) -> None:
        self._client.close()
