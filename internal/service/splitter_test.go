package service

import (
	"testing"

	"github.com/saufi-opi/lybrix/internal/queue"
)

// parseJobsFor is the splitter's enqueue-payload seam (R-32): pure, so the
// job fan-out is testable without a DB or Redis.
func TestParseJobsFor(t *testing.T) {
	cases := []struct {
		name      string
		docID     string
		sourceURI string
		bounds    [][2]int
		want      []queue.ParseJob
	}{
		{
			name:      "three bounds → idx sequence 0..2, page passthrough",
			docID:     "11111111-1111-1111-1111-111111111111",
			sourceURI: "s3://raw/book.pdf",
			bounds:    [][2]int{{1, 16}, {17, 32}, {33, 48}},
			want: []queue.ParseJob{
				{SchemaVersion: queue.SchemaVersion, DocID: "11111111-1111-1111-1111-111111111111",
					Idx: 0, PageStart: 1, PageEnd: 16, SourceURI: "s3://raw/book.pdf"},
				{SchemaVersion: queue.SchemaVersion, DocID: "11111111-1111-1111-1111-111111111111",
					Idx: 1, PageStart: 17, PageEnd: 32, SourceURI: "s3://raw/book.pdf"},
				{SchemaVersion: queue.SchemaVersion, DocID: "11111111-1111-1111-1111-111111111111",
					Idx: 2, PageStart: 33, PageEnd: 48, SourceURI: "s3://raw/book.pdf"},
			},
		},
		{
			name:      "single synthetic shard (idx 0, pages 1-1)",
			docID:     "22222222-2222-2222-2222-222222222222",
			sourceURI: "s3://raw/book.epub",
			bounds:    [][2]int{{1, 1}},
			want: []queue.ParseJob{
				{SchemaVersion: queue.SchemaVersion, DocID: "22222222-2222-2222-2222-222222222222",
					Idx: 0, PageStart: 1, PageEnd: 1, SourceURI: "s3://raw/book.epub"},
			},
		},
		{
			name:      "empty bounds → empty slice",
			docID:     "33333333-3333-3333-3333-333333333333",
			sourceURI: "s3://raw/empty.pdf",
			bounds:    [][2]int{},
			want:      []queue.ParseJob{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseJobsFor(tc.docID, tc.sourceURI, tc.bounds)
			if len(got) != len(tc.want) {
				t.Fatalf("count drift: got %d, want %d (%+v)", len(got), len(tc.want), got)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("job %d drift:\n got %+v\nwant %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}
