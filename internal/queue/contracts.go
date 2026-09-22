// Package queue: versioned job payloads for every Redis Stream (PRD §4.2).
//
// Queue payloads are a wire contract between services that deploy
// independently — hence an explicit SchemaVersion on every message, so an
// old consumer can reject (or tolerate) a newer producer instead of
// misreading its fields. JSON tags are identical to 1.0's contracts.py.
package queue

import "encoding/json"

// SchemaVersion bumps on any breaking change to a payload's fields.
const SchemaVersion = 1

// SplitJob is the doc.split payload — produced by the API on commit.
type SplitJob struct {
	SchemaVersion int    `json:"schema_version"`
	DocID         string `json:"doc_id"`
	SourceURI     string `json:"source_uri"`
}

// ParseJob is the doc.parse payload — produced by the splitter, one per shard.
type ParseJob struct {
	SchemaVersion int    `json:"schema_version"`
	DocID         string `json:"doc_id"`
	Idx           int    `json:"idx"`
	PageStart     int    `json:"page_start"`
	PageEnd       int    `json:"page_end"`
	SourceURI     string `json:"source_uri"`
}

// ValidatePages enforces page_end >= page_start (1.0 contracts.py).
func (j *ParseJob) ValidatePages() error {
	if j.PageEnd < j.PageStart {
		return &PayloadError{Field: "page_end", Msg: "page_end < page_start"}
	}
	return nil
}

// EmbedJob is the doc.embed payload — produced when a document's last
// shard settles.
type EmbedJob struct {
	SchemaVersion int    `json:"schema_version"`
	DocID         string `json:"doc_id"`
}

// PayloadError marks a structurally invalid job payload.
type PayloadError struct {
	Field string
	Msg   string
}

func (e *PayloadError) Error() string { return e.Field + ": " + e.Msg }

// MarshalJob is the single serializer every producer uses.
func MarshalJob(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
