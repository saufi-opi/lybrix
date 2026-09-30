package objectstore

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// statusErr is a hand-built stand-in for the SDK's http-status-carrying
// errors (smithyhttp.ResponseError shape).
type statusErr struct {
	code int
}

func (e *statusErr) Error() string       { return "http response error" }
func (e *statusErr) HTTPStatusCode() int { return e.code }

// IsNotFound classifies object-gone errors only (BACKLOG R-39): modeled
// NoSuchKey/NotFound, generic smithy API errors carrying 404-style codes,
// and 404-class HTTP status errors. Everything else — 500-class API errors,
// transport failures, wrapped non-API errors — must read as transient.
func TestIsNotFound(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"modeled NoSuchKey", &types.NoSuchKey{Message: strPtr("no such key")}, true},
		{"modeled NotFound", &types.NotFound{}, true},
		{"generic API NoSuchKey", &smithy.GenericAPIError{Code: "NoSuchKey", Message: "gone"}, true},
		{"generic API NotFound", &smithy.GenericAPIError{Code: "NotFound", Message: "gone"}, true},
		{"404 http status", &statusErr{code: http.StatusNotFound}, true},
		{"500 http status", &statusErr{code: http.StatusInternalServerError}, false},
		{"API 500 InternalError", &smithy.GenericAPIError{Code: "InternalError", Message: "boom"}, false},
		{"API 503 SlowDown", &smithy.GenericAPIError{Code: "SlowDown", Message: "slow"}, false},
		{"transport error", errors.New("dial tcp: connection reset by peer"), false},
		{"wrapped NoSuchKey", fmt.Errorf("download failed: %w", &types.NoSuchKey{Message: strPtr("nope")}), true},
		{"wrapped operation error carrying 404", fmt.Errorf("operation error S3: GetObject: %w",
			&smithyhttp.ResponseError{Response: &smithyhttp.Response{Response: &http.Response{StatusCode: 404}}, Err: &statusErr{code: 404}}), true},
		{"wrapped non-API under API error", fmt.Errorf("op error: %w", &smithy.GenericAPIError{Code: "InternalError"}), false},
	}
	for _, tc := range cases {
		if got := IsNotFound(tc.err); got != tc.want {
			t.Errorf("%s: IsNotFound=%v, want %v (err=%v)", tc.name, got, tc.want, tc.err)
		}
	}
}

func strPtr(s string) *string { return &s }
