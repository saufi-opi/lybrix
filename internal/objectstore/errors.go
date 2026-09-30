// Package objectstore: error classification for S3/MinIO failures.
//
// IsNotFound separates "the object is genuinely gone" from a transient S3
// blip (BACKLOG R-39): only the former is terminal for a document — a
// vanished raw/parsed object can never be re-fetched, while a 503 or
// connection reset must stay retryable.
package objectstore

import (
	"errors"

	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// IsNotFound reports whether err is an S3/MinIO "object does not exist"
// error: the modeled NoSuchKey/NotFound exceptions, or any smithy API error
// carrying one of the 404-style codes. MinIO's S3 layer returns NoSuchKey;
// some paths surface the generic NotFound. A wrapped chain is walked, and
// anything that is not an API error (transport failure, timeouts, …) is
// NOT found-classified — callers must keep treating it as transient.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	var noKey *s3types.NoSuchKey
	if errors.As(err, &noKey) {
		return true
	}
	var nf *s3types.NotFound
	if errors.As(err, &nf) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound":
			return true
		}
		return false
	}
	// Some paths (MinIO's gateway, the downloader's wrappers) surface a bare
	// HTTP status instead of a modeled error — an http-status-carrying error
	// in the chain with 404 means the object is gone. Anything else is
	// transient.
	var httpErr interface{ HTTPStatusCode() int }
	if errors.As(err, &httpErr) {
		return httpErr.HTTPStatusCode() == 404
	}
	return false
}
