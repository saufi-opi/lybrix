package store

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ErrInvalidMetadataFilter is the typed validation failure — the API maps
// it to 422 so the caller can correct the filter.
var ErrInvalidMetadataFilter = errors.New("invalid metadata_filter")

// safeFilterKey restricts metadata keys to identifiers only — the key is
// interpolated into SQL as a literal ('->>' path string), so anything
// beyond [A-Za-z0-9_] is refused rather than escaped (422 at the API layer).
var safeFilterKey = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

// yearRangeGuard is emitted before the ::numeric cast: Postgres evaluates
// AND per row, so non-numeric values fail the predicate instead of raising
// "invalid input syntax for type numeric" (one {"year": "n/a"} row must not
// 500 every filtered search).
const yearRangeGuard = `^-?[0-9]+(\.[0-9]+)?$`

// BuildMetadataFilter compiles the simple-map dialect into a parameterized
// SQL predicate over documents (aliased `df` by the callers). Keys:
//
//	"author"    → lower(df.author) = lower($n)   (case-insensitive)
//	"year_from" → regex-guarded (df.metadata->>'year')::numeric >= $n
//	"year_to"   → regex-guarded (df.metadata->>'year')::numeric <= $n
//	other       → df.metadata->>'key' = $n       (text equality)
//
// Values bind as parameters — no user data is ever interpolated. Unknown
// keys must match ^[A-Za-z0-9_]{1,64}$; anything else (or a range value
// that is not a number) is an error. Empty/nil filter → ("", nil).
func BuildMetadataFilter(f map[string]any) (string, []any, error) {
	if len(f) == 0 {
		return "", nil, nil
	}
	conds := make([]string, 0, len(f))
	var args []any
	addParam := func(v any) string {
		args = append(args, v)
		return strconv.Itoa(len(args))
	}
	for _, key := range sortedKeys(f) {
		switch key {
		case "author":
			s, ok := f[key].(string)
			if !ok {
				return "", nil, fmt.Errorf("%w: author must be a string", ErrInvalidMetadataFilter)
			}
			n := addParam(s)
			conds = append(conds, "lower(df.author) = lower($"+n+")")
		case "year_from", "year_to":
			num, err := filterNumber(f[key])
			if err != nil {
				return "", nil, fmt.Errorf("%w: %s must be a number", ErrInvalidMetadataFilter, key)
			}
			n := addParam(num)
			cmp := ">="
			if key == "year_to" {
				cmp = "<="
			}
			conds = append(conds,
				"(df.metadata->>'year') ~ '"+yearRangeGuard+"'"+
					" AND (df.metadata->>'year')::numeric "+cmp+" $"+n)
		default:
			if !safeFilterKey.MatchString(key) {
				return "", nil, fmt.Errorf("%w: key %q must match ^[A-Za-z0-9_]{1,64}$",
					ErrInvalidMetadataFilter, key)
			}
			s, ok := f[key].(string)
			if !ok {
				return "", nil, fmt.Errorf("%w: %s must be a string value", ErrInvalidMetadataFilter, key)
			}
			// the key is a validated identifier — safe to inline as a literal
			n := addParam(s)
			conds = append(conds, "df.metadata->>'"+key+"' = $"+n)
		}
	}
	// No leading " AND ": every search template splices this fragment as
	// "($n = '' OR <fragment>)" (R-14 push-down), so a leading AND produced
	// "(… OR  AND EXISTS(…))" — SQLSTATE 42601 on every filtered search.
	return "EXISTS (SELECT 1 FROM documents df WHERE df.id = chunks.doc_id AND " +
		strings.Join(conds, " AND ") + ")", args, nil
}

// filterNumber coerces JSON numbers (float64) and numeric strings.
func filterNumber(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case int:
		return float64(n), nil
	case string:
		return strconv.ParseFloat(n, 64)
	}
	return 0, fmt.Errorf("not a number")
}

// filterParamRe matches the fragment's own placeholders. The fragment is
// compiled with numbering from $1, but every search template already binds
// its base params ($1..), so the store layer shifts the fragment's numbers
// past them. Only placeholders match: values are bound parameters, the
// regex guard is an inlined literal without '$', and keys are validated
// identifiers — no other '$' can appear in the fragment.
var filterParamRe = regexp.MustCompile(`\$(\d+)`)

// renumberFilterParams shifts the compiled metadata filter's placeholders
// past the search template's base params (HybridSearch binds $1..$6, the
// dense/bm25 legs $1..$5). An empty fragment passes through unchanged.
// Without the shift, the fragment's $1/$2 collide with the query vector and
// collection params — Postgres resolves one type per placeholder, so the
// filter's float8 args arrived typed as the base params' types and every
// filtered search died with 42883 "numeric <= text".
func renumberFilterParams(fragment string, base int) string {
	if fragment == "" {
		return ""
	}
	return filterParamRe.ReplaceAllStringFunc(fragment, func(m string) string {
		n, err := strconv.Atoi(m[1:])
		if err != nil {
			return m
		}
		return "$" + strconv.Itoa(base+n)
	})
}

// sortedKeys gives deterministic SQL (test assertions + stable plans).
func sortedKeys(f map[string]any) []string {
	out := make([]string, 0, len(f))
	for k := range f {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
