"""RSS guard: soft OOM budget self-check (PRD §6.3 memory discipline).

Self-check at the soft budget and raise SoftOOM — a clean retry with a
traceback beats an opaque SIGKILL from the 8g container limit, which is
a backstop only: if it fires you have a bug.
"""

from __future__ import annotations

import resource

from core.config import Settings, get_settings
from core.errors import ErrorCode, PlatformError


def current_rss_mb() -> int:
    """Peak RSS of this process in MB (ru_maxrss is KB on Linux)."""
    return int(resource.getrusage(resource.RUSAGE_SELF).ru_maxrss // 1024)


class SoftOOM(PlatformError):
    def __init__(self, rss_mb: int, budget_mb: int) -> None:
        super().__init__(
            code=ErrorCode.SHARD_OOM,
            detail=f"peak RSS {rss_mb}MB exceeded soft budget {budget_mb}MB",
        )
        self.rss_mb = rss_mb
        self.budget_mb = budget_mb


def check_rss_budget(settings: Settings | None = None) -> int:
    """Raise SoftOOM when the process's peak RSS crosses the soft budget.

    Called between pipeline steps inside the parser; returns current
    peak RSS MB for logging/metrics.
    """
    s = settings or get_settings()
    rss = current_rss_mb()
    if rss > s.parser_soft_rss_mb:
        raise SoftOOM(rss, s.parser_soft_rss_mb)
    return rss
