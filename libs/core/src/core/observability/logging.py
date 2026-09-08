"""structlog JSON logging setup (PRD §10.3).

Structured JSON to stdout with ``doc_id``, ``shard_idx``, ``worker_id``
and ``stage`` on every line. Docker's json-file driver with rotation is
the only collector — no separate log stack in v1.
"""

from __future__ import annotations

import logging
import sys

import structlog


def configure_logging(level: str = "INFO") -> None:
    """Idempotent structlog setup: JSON lines to stdout, ISO timestamps,
    level on every record. Call once at process start."""
    logging.basicConfig(
        format="%(message)s",
        stream=sys.stdout,
        level=level.upper(),
        force=True,
    )
    structlog.configure(
        processors=[
            structlog.contextvars.merge_contextvars,
            structlog.processors.add_log_level,
            structlog.processors.TimeStamper(fmt="iso", utc=True),
            structlog.processors.StackInfoRenderer(),
            structlog.processors.format_exc_info,
            structlog.processors.JSONRenderer(),
        ],
        wrapper_class=structlog.make_filtering_bound_logger(
            logging.getLevelName(level.upper())
            if isinstance(logging.getLevelName(level.upper()), int)
            else logging.INFO
        ),
        logger_factory=structlog.PrintLoggerFactory(),
        cache_logger_on_first_use=True,
    )


def get_logger(**initial_context):
    return structlog.get_logger(**initial_context)
