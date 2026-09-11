"""Error taxonomy (PRD §6.7) drives retryability — not string matching."""

from __future__ import annotations

from core.errors import ERROR_SPECS, ErrorCode, PlatformError, Stage


def test_all_error_codes_have_specs():
    assert set(ERROR_SPECS) == set(ErrorCode)
    assert len(ERROR_SPECS) == 10  # +DOC_EMBED_FAILED (2026-09-11 retry cap)


def test_non_retryable_codes():
    assert ERROR_SPECS[ErrorCode.PDF_ENCRYPTED].retryable is False
    assert ERROR_SPECS[ErrorCode.PDF_CORRUPT].retryable is False
    assert ERROR_SPECS[ErrorCode.EMBED_DIM_MISMATCH].retryable is False
    assert ERROR_SPECS[ErrorCode.DEDUPE_CONFLICT].retryable is False
    assert ERROR_SPECS[ErrorCode.DOC_EMBED_FAILED].retryable is False


def test_oom_is_retryable_and_parse_stage():
    spec = ERROR_SPECS[ErrorCode.SHARD_OOM]
    assert spec.retryable is True
    assert spec.stage == Stage.PARSE
    assert "retry" in spec.ui_treatment.lower()


def test_platform_error_carries_code_and_retryability():
    err = PlatformError(ErrorCode.SHARD_OOM, "6.4GB > 6GB budget")
    assert err.code == ErrorCode.SHARD_OOM
    assert err.retryable is True
    assert "6.4GB" in str(err)


def test_every_code_has_ui_treatment():
    for code, spec in ERROR_SPECS.items():
        assert spec.ui_treatment, f"{code} missing UI treatment"
