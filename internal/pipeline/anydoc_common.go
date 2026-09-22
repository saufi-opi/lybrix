package pipeline

import "errors"

// ErrAnyDocUnavailable means the fast path is not usable. In the stub build
// it is the Parse result; on a `-tags anydoc` build the parser never sees
// it (the CGO engine is Available()). The parser treats it as "fast path
// unavailable" and falls through to docling-serve.
var ErrAnyDocUnavailable = errors.New("anydoc: not linked (built without -tags anydoc)")
