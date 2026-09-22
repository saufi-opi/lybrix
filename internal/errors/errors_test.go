package errors

import "testing"

// codes string-match 1.0 values; retryable map matches ERROR_SPECS
// (libs/core/errors.py).
func TestCodeValues(t *testing.T) {
	want := map[ErrorCode]string{
		CodePDFEncrypted:       "PDF_ENCRYPTED",
		CodePDFCorrupt:         "PDF_CORRUPT",
		CodeShardOOM:           "SHARD_OOM",
		CodeShardTimeout:       "SHARD_TIMEOUT",
		CodeOCRFailed:          "OCR_FAILED",
		CodeEmbedUnavailable:   "EMBED_UNAVAILABLE",
		CodeEmbedDimMismatch:   "EMBED_DIM_MISMATCH",
		CodeVectorUpsertFailed: "VECTOR_UPSERT_FAILED",
		CodeDedupeConflict:     "DEDUPE_CONFLICT",
		CodeDocEmbedFailed:     "DOC_EMBED_FAILED",
	}
	for code, s := range want {
		if string(code) != s {
			t.Fatalf("code %s drifted to %s", s, string(code))
		}
	}
}

func TestRetryableMap(t *testing.T) {
	// (code, retryable) pairs straight from 1.0's ERROR_SPECS.
	specs := []struct {
		code      ErrorCode
		retryable bool
	}{
		{CodePDFEncrypted, false},
		{CodePDFCorrupt, false},
		{CodeShardOOM, true},
		{CodeShardTimeout, true},
		{CodeOCRFailed, true},
		{CodeEmbedUnavailable, true},
		{CodeEmbedDimMismatch, false},
		{CodeVectorUpsertFailed, true},
		{CodeDedupeConflict, false},
		{CodeDocEmbedFailed, false},
	}
	for _, s := range specs {
		err := NewPlatformError(s.code, "test")
		if err.Retryable() != s.retryable {
			t.Fatalf("%s retryable = %v, want %v", s.code, err.Retryable(), s.retryable)
		}
	}
}

func TestErrorMessageShape(t *testing.T) {
	err := NewPlatformError(CodeShardTimeout, "shard took too long")
	if err.Error() != "SHARD_TIMEOUT: shard took too long" {
		t.Fatalf("message shape drifted: %q", err.Error())
	}
	err = NewPlatformError(CodePDFCorrupt, "")
	if err.Error() != "PDF_CORRUPT" {
		t.Fatalf("bare code message drifted: %q", err.Error())
	}
}
