"""In-process metrics counters (PRD §10).

v1 keeps metrics in-process, exposed as JSON at /v1/system/metrics in a
shape trivially convertible to Prometheus text format, so the eventual
migration (§10.4) is a serializer swap, not a rewrite.
"""

from __future__ import annotations

import threading
import time
from collections import defaultdict
from dataclasses import dataclass, field


@dataclass
class Metrics:
    started_at: float = field(default_factory=time.time)
    _counters: dict[str, int] = field(default_factory=lambda: defaultdict(int))
    _lock: threading.Lock = field(default_factory=threading.Lock)

    def incr(self, name: str, amount: int = 1) -> None:
        with self._lock:
            self._counters[name] += amount

    def snapshot(self) -> dict:
        with self._lock:
            return {
                "uptime_s": round(time.time() - self.started_at, 1),
                "counters": dict(self._counters),
            }


_metrics = Metrics()


def metrics() -> Metrics:
    return _metrics
