package service

import (
	"testing"

	"github.com/saufi-opi/lybrix/internal/pipeline"
)

// TestAssembleEmbeddingRows pins the phase-E→W seam (R-36 task 4): batch
// vectors concatenate in order onto chunkIDs, aligned by the running offset
// PlanBatches' output shape guarantees. Misalignment is impossible by
// construction — these cases pin that property, including the empty and
// multi-batch shapes.
// vec builds a distinct single-element vector for alignment stamping.
func vec(v float32) []float32 { return []float32{v} }

func TestAssembleEmbeddingRows(t *testing.T) {
	cases := []struct {
		name           string
		chunkIDs       []string
		batches        [][]string
		vectorsByBatch [][][]float32
		want           [][2]any // (chunkID, vector[0]) pairs
	}{
		{
			name:           "empty batches → empty rows",
			chunkIDs:       []string{},
			batches:        [][]string{},
			vectorsByBatch: [][][]float32{},
			want:           [][2]any{},
		},
		{
			name:           "single batch, aligned",
			chunkIDs:       []string{"c0", "c1", "c2"},
			batches:        [][]string{{"a", "b", "c"}},
			vectorsByBatch: [][][]float32{{{1}, {2}, {3}}},
			want:           [][2]any{{"c0", float32(1)}, {"c1", float32(2)}, {"c2", float32(3)}},
		},
		{
			name:     "multiple batches concatenate in order",
			chunkIDs: []string{"c0", "c1", "c2", "c3"},
			batches:  [][]string{{"a", "b"}, {"c", "d"}},
			vectorsByBatch: [][][]float32{
				{{10}, {11}},
				{{12}, {13}},
			},
			want: [][2]any{{"c0", float32(10)}, {"c1", float32(11)}, {"c2", float32(12)}, {"c3", float32(13)}},
		},
		{
			name:           "ragged last batch (PlanBatches tail)",
			chunkIDs:       []string{"c0", "c1", "c2"},
			batches:        [][]string{{"a", "b"}, {"c"}},
			vectorsByBatch: [][][]float32{{{1}, {2}}, {{3}}},
			want:           [][2]any{{"c0", float32(1)}, {"c1", float32(2)}, {"c2", float32(3)}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := assembleEmbeddingRows(tc.chunkIDs, tc.batches, tc.vectorsByBatch)
			if len(got) != len(tc.want) {
				t.Fatalf("row count drift: got %d, want %d (%+v)", len(got), len(tc.want), got)
			}
			for i, pair := range tc.want {
				wantID := pair[0].(string)
				wantV := pair[1].(float32)
				if got[i].ChunkID != wantID {
					t.Fatalf("row %d chunkID drift: %q, want %q", i, got[i].ChunkID, wantID)
				}
				if len(got[i].Vector) == 0 || got[i].Vector[0] != wantV {
					t.Fatalf("row %d vector drift: %v (want [0]=%v)", i, got[i].Vector, wantV)
				}
			}
		})
	}
}

// TestAssembleEmbeddingRowsAlignmentWithPlanBatches mirrors the existing
// PlanBatches fixtures: assemble must pair EVERY text batched by
// PlanBatches with exactly one row, in the original text order — the
// alignment property the write tx depends on (a misaligned pair would write
// chunk A's vector onto chunk B's row).
func TestAssembleEmbeddingRowsAlignmentWithPlanBatches(t *testing.T) {
	// 5 texts of varying token counts across the batch boundary — same
	// fixture shape as pipeline's PlanBatches tests.
	texts := []string{"one two", "three four five six", "seven", "eight nine ten", "eleven"}
	batches, _ := pipeline.PlanBatches(texts, pipeline.WhitespaceTokenizer{}, 4, 48)

	chunkIDs := make([]string, len(texts))
	for i := range texts {
		chunkIDs[i] = "id-" + texts[i]
	}
	vectorsByBatch := make([][][]float32, len(batches))
	// stamp each vector with its global text index so alignment is provable
	idx := 0
	for i, batch := range batches {
		vectorsByBatch[i] = make([][]float32, len(batch))
		for j := range batch {
			vectorsByBatch[i][j] = vec(float32(idx))
			idx++
		}
	}

	rows := assembleEmbeddingRows(chunkIDs, batches, vectorsByBatch)
	if len(rows) != len(texts) {
		t.Fatalf("alignment drift: %d rows for %d texts", len(rows), len(texts))
	}
	for i, row := range rows {
		if row.ChunkID != chunkIDs[i] {
			t.Fatalf("row %d paired with %q, want %q", i, row.ChunkID, chunkIDs[i])
		}
		if row.Vector[0] != float32(i) {
			t.Fatalf("row %d carries vector %v, want its own index %d", i, row.Vector, i)
		}
	}
}
