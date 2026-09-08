"""The error taxonomy from PRD §6.7.

Error codes are a product surface, not an implementation detail — they
drive the UI's retry affordances, so they live in the shared library
next to the retryability metadata rather than scattered across workers.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import StrEnum


class Stage(StrEnum):
    SPLIT = "split"
    PARSE = "parse"
    EMBED = "embed"
    INDEX = "index"


@dataclass(frozen=True)
class ErrorSpec:
    code: ErrorCode
    stage: Stage
    retryable: bool
    ui_treatment: str


class ErrorCode(StrEnum):
    PDF_ENCRYPTED = "PDF_ENCRYPTED"
    PDF_CORRUPT = "PDF_CORRUPT"
    SHARD_OOM = "SHARD_OOM"
    SHARD_TIMEOUT = "SHARD_TIMEOUT"
    OCR_FAILED = "OCR_FAILED"
    EMBED_UNAVAILABLE = "EMBED_UNAVAILABLE"
    EMBED_DIM_MISMATCH = "EMBED_DIM_MISMATCH"
    VECTOR_UPSERT_FAILED = "VECTOR_UPSERT_FAILED"
    DEDUPE_CONFLICT = "DEDUPE_CONFLICT"


ERROR_SPECS: dict[ErrorCode, ErrorSpec] = {
    spec.code: spec
    for spec in (
        ErrorSpec(
            code=ErrorCode.PDF_ENCRYPTED,
            stage=Stage.SPLIT,
            retryable=False,
            ui_treatment="Password required — prompt for upload replacement",
        ),
        ErrorSpec(
            code=ErrorCode.PDF_CORRUPT,
            stage=Stage.SPLIT,
            retryable=False,
            ui_treatment="Terminal, offer delete",
        ),
        ErrorSpec(
            code=ErrorCode.SHARD_OOM,
            stage=Stage.PARSE,
            retryable=True,
            ui_treatment="Auto retry ladder, show attempt count",
        ),
        ErrorSpec(
            code=ErrorCode.SHARD_TIMEOUT,
            stage=Stage.PARSE,
            retryable=True,
            ui_treatment="Auto",
        ),
        ErrorSpec(
            code=ErrorCode.OCR_FAILED,
            stage=Stage.PARSE,
            retryable=True,
            ui_treatment="Auto, flags reduced quality",
        ),
        ErrorSpec(
            code=ErrorCode.EMBED_UNAVAILABLE,
            stage=Stage.EMBED,
            retryable=True,
            ui_treatment="Auto, banner: Embedding service down",
        ),
        ErrorSpec(
            code=ErrorCode.EMBED_DIM_MISMATCH,
            stage=Stage.EMBED,
            retryable=False,
            ui_treatment="Config error — collection model vs. TEI model",
        ),
        ErrorSpec(
            code=ErrorCode.VECTOR_UPSERT_FAILED,
            stage=Stage.INDEX,
            retryable=True,
            ui_treatment="Auto",
        ),
        ErrorSpec(
            code=ErrorCode.DEDUPE_CONFLICT,
            stage=Stage.SPLIT,
            retryable=False,
            ui_treatment="Link to the existing document",
        ),
    )
}


class PlatformError(Exception):
    """Base for every error the platform raises deliberately.

    Carries the taxonomy code plus free-form detail; workers catch this,
    write an ``events`` row, and drive the retry ladder from
    ``ERROR_SPECS[code].retryable`` instead of string-matching messages.
    """

    def __init__(self, code: ErrorCode, detail: str = "") -> None:
        self.code = code
        self.detail = detail
        spec = ERROR_SPECS.get(code)
        self.retryable = spec.retryable if spec else True
        super().__init__(f"{code.value}: {detail}" if detail else code.value)
