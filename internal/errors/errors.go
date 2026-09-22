// Package errors holds the error taxonomy (PRD §6.7).
//
// Error codes are a product surface, not an implementation detail — they
// drive the UI's retry affordances, so they live in a shared package next
// to the retryability metadata rather than scattered across workers.
package errors

import "fmt"

// Stage is the pipeline arrow a code belongs to.
type Stage string

const (
	StageSplit Stage = "split"
	StageParse Stage = "parse"
	StageEmbed Stage = "embed"
	StageIndex Stage = "index"
)

// ErrorCode is the exact string value a 1.0 events row carried — these
// strings are a wire contract with the web UI's retry affordances.
type ErrorCode string

const (
	CodePDFEncrypted       ErrorCode = "PDF_ENCRYPTED"
	CodePDFCorrupt         ErrorCode = "PDF_CORRUPT"
	CodeShardOOM           ErrorCode = "SHARD_OOM"
	CodeShardTimeout       ErrorCode = "SHARD_TIMEOUT"
	CodeOCRFailed          ErrorCode = "OCR_FAILED"
	CodeEmbedUnavailable   ErrorCode = "EMBED_UNAVAILABLE"
	CodeEmbedDimMismatch   ErrorCode = "EMBED_DIM_MISMATCH"
	CodeVectorUpsertFailed ErrorCode = "VECTOR_UPSERT_FAILED"
	CodeDedupeConflict     ErrorCode = "DEDUPE_CONFLICT"
	CodeDocEmbedFailed     ErrorCode = "DOC_EMBED_FAILED"
	CodeDLQ                ErrorCode = "DLQ"
	CodeShardResplit       ErrorCode = "SHARD_RESPLIT"
)

// ErrorSpec mirrors 1.0 ERROR_SPECS: stage + retryability + UI treatment.
type ErrorSpec struct {
	Code        ErrorCode
	Stage       Stage
	Retryable   bool
	UITreatment string
}

// ErrorSpecs is the retryability map; retryability comes from this table,
// never from string matching on messages.
var ErrorSpecs = map[ErrorCode]ErrorSpec{
	CodePDFEncrypted: {CodePDFEncrypted, StageSplit, false, "Password required — prompt for upload replacement"},
	CodePDFCorrupt:   {CodePDFCorrupt, StageSplit, false, "Terminal, offer delete"},
	CodeShardOOM:     {CodeShardOOM, StageParse, true, "Auto retry ladder, show attempt count"},
	CodeShardTimeout: {CodeShardTimeout, StageParse, true, "Auto"},
	CodeOCRFailed:    {CodeOCRFailed, StageParse, true, "Auto, flags reduced quality"},
	CodeEmbedUnavailable: {
		CodeEmbedUnavailable, StageEmbed, true, "Auto, banner: Embedding service down",
	},
	CodeEmbedDimMismatch: {
		CodeEmbedDimMismatch, StageEmbed, false, "Config error — collection model vs. TEI model",
	},
	CodeVectorUpsertFailed: {CodeVectorUpsertFailed, StageIndex, true, "Auto"},
	CodeDedupeConflict:     {CodeDedupeConflict, StageSplit, false, "Link to the existing document"},
	CodeDocEmbedFailed: {
		CodeDocEmbedFailed, StageEmbed, false, "Terminal — embed failed after max attempts, offer re-ingest",
	},
}

// PlatformError is the error every deliberately-raised worker failure uses.
// Handlers return it; the runner records an events row and the retry ladder
// reads Retryable() from the taxonomy, never from message text.
type PlatformError struct {
	Code      ErrorCode
	Detail    string
	retryable bool
}

// NewPlatformError builds an error with retryability resolved from the table.
func NewPlatformError(code ErrorCode, detail string) *PlatformError {
	spec, ok := ErrorSpecs[code]
	return &PlatformError{Code: code, Detail: detail, retryable: !ok || spec.Retryable}
}

func (e *PlatformError) Error() string {
	if e.Detail == "" {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Detail)
}

func (e *PlatformError) Retryable() bool { return e.retryable }
