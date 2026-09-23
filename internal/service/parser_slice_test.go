package service

import (
	"testing"

	"github.com/saufi-opi/lybrix/internal/pipeline"
)

// Table test for the slice decision (BACKLOG R-22): PDF + a real sub-range
// slices; full-doc spans, non-PDF formats, and unknown ranges do not.
func TestShouldSlicePDF(t *testing.T) {
	cases := []struct {
		name       string
		format     pipeline.Format
		pageStart  int
		pageEnd    int
		totalPages int
		want       bool
	}{
		{"pdf mid-range", pipeline.FmtPDF, 5, 24, 300, true},
		{"pdf full doc", pipeline.FmtPDF, 1, 300, 300, false},
		{"pdf full doc overshoot", pipeline.FmtPDF, 1, 320, 300, false},
		{"pdf single page not first", pipeline.FmtPDF, 7, 7, 300, true},
		{"pdf single page one", pipeline.FmtPDF, 1, 1, 300, true},
		{"pdf zero range (unknown)", pipeline.FmtPDF, 0, 0, 300, false},
		{"pdf zero start", pipeline.FmtPDF, 0, 24, 300, false},
		{"pdf reversed range", pipeline.FmtPDF, 24, 5, 300, false},
		{"epub", pipeline.FmtEPUB, 5, 24, 300, false},
		{"docx", pipeline.FmtDOCX, 0, 0, 1, false},
		{"txt", pipeline.FmtTXT, 1, 5, 5, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldSlicePDF(tc.format, tc.pageStart, tc.pageEnd, tc.totalPages)
			if got != tc.want {
				t.Fatalf("shouldSlicePDF(%v, %d, %d, %d) = %v, want %v",
					tc.format, tc.pageStart, tc.pageEnd, tc.totalPages, got, tc.want)
			}
		})
	}
}
