package pipeline

import "strings"

func fieldsOf(s string) []string   { return strings.Fields(s) }
func joinWords(xs []string) string { return strings.Join(xs, " ") }
