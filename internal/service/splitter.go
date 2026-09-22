// Package service: the four worker handlers — splitter, parser, embedder —
// wiring internal/pipeline onto internal/store and internal/queue. This is
// the Go counterpart of services/workers.
package service

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5"

	"github.com/saufi-opi/lybrix/internal/config"

	"github.com/saufi-opi/lybrix/internal/errors"
	"github.com/saufi-opi/lybrix/internal/objectstore"
	"github.com/saufi-opi/lybrix/internal/pipeline"
	"github.com/saufi-opi/lybrix/internal/queue"
	"github.com/saufi-opi/lybrix/internal/store"
)

// Deps is the shared worker wiring.
type Deps struct {
	Settings *config.Settings
	DB       *store.DB
	Redis    queue.Cmdable2
	S3       *objectstore.Client
	Parser   *pipeline.TwoTierParser
}

// HandleSplit is the splitter handler (PRD §6.2): cheap, CPU-only, never
// touches a heavy parser. Downloads the source once, extracts the page
// count + outline, writes shard rows, and fans out one doc.parse job per
// shard.
func HandleSplit(ctx context.Context, deps Deps, tx pgx.Tx, job map[string]any) error {
	s := deps.Settings
	docID, err := jobString(job, "doc_id")
	if err != nil {
		return err
	}
	doc, err := deps.DB.GetDocument(ctx, docID)
	if err != nil {
		return err
	}
	if doc == nil {
		return errors.NewPlatformError(errors.CodePDFCorrupt, fmt.Sprintf("document %s vanished", docID))
	}

	// Idempotency: re-delivered split jobs (janitor requeue / PEL reclaim)
	// must not re-insert shard rows. Shards PK is (doc_id, idx) — probe with
	// the composite key. If shards exist this doc is already split — just
	// advance UPLOADED → PARSING so the state machine converges.
	already, err := deps.DB.GetShard(ctx, docID, 0)
	if err != nil {
		return err
	}
	if already != nil {
		if doc.State == store.StateUploaded {
			return deps.DB.SetDocState(ctx, tx, docID, store.StateParsing, nil, nil)
		}
		return nil
	}

	tmpDir, err := os.MkdirTemp("", "split-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	localPath := filepath.Join(tmpDir, "source.pdf")
	bucket := s.S3BucketRaw
	key := objectstore.RawKey(docID)
	if err := deps.S3.DownloadTo(ctx, bucket, key, localPath); err != nil {
		return errors.NewPlatformError(errors.CodePDFCorrupt, fmt.Sprintf("source missing: %v", err))
	}

	// EPUB branch (PLAN.md item 19): pdfcpu would raise PDF_CORRUPT on a
	// zip container, so the EPUB path skips PageCount/ExtractBookmarks
	// entirely and emits exactly ONE synthetic shard (idx=0, pages 1-1) —
	// the parser routes it straight to docling-serve, which parses EPUB
	// natively, and treats the whole book as one "page".
	if doc.MimeType != nil && *doc.MimeType == "application/epub+zip" {
		return splitEpubSingleShard(ctx, deps, tx, doc, tmpDir)
	}

	pageCount, err := pipeline.PageCount(localPath)
	if err != nil {
		if code := pipeline.ClassifyPDFError(err); code == "PDF_ENCRYPTED" {
			return errors.NewPlatformError(errors.CodePDFEncrypted, "source PDF is encrypted")
		}
		return errors.NewPlatformError(errors.CodePDFCorrupt, fmt.Sprintf("cannot open PDF: %v", err))
	}
	outline := ExtractBookmarks(localPath)

	var bounds []pipeline.ShardBound
	if len(outline) > 0 {
		bounds, err = pipeline.ChapterAlignedBounds(outline, pageCount, s.ShardPages)
	} else {
		bounds, err = pipeline.FixedBounds(pageCount, s.ShardPages, 1)
	}
	if err != nil {
		return err
	}

	if err := deps.DB.SetDocState(ctx, tx, docID, store.StateParsing, nil, nil); err != nil {
		return err
	}
	totalShards := len(bounds)
	if err := updateSplitMeta(ctx, tx, docID, totalShards, pageCount); err != nil {
		return err
	}
	if _, err := deps.DB.InsertShards(ctx, tx, docID, toBoundPairs(bounds), 0); err != nil {
		return err
	}
	for _, b := range bounds {
		job := queue.ParseJob{
			SchemaVersion: queue.SchemaVersion,
			DocID:         docID,
			Idx:           b.Idx,
			PageStart:     b.PageStart,
			PageEnd:       b.PageEnd,
			SourceURI:     doc.SourceURI,
		}
		if _, err := queue.XAddJob(ctx, deps.Redis, queue.StreamParse, job); err != nil {
			return err
		}
	}
	slog.Info("split complete", "doc", docID, "pages", pageCount, "shards", totalShards)
	return nil
}

// splitEpubSingleShard is the EPUB splitter tail: advance UPLOADED →
// PARSING, write one synthetic shard (idx=0, page_start=1, page_end=1),
// enqueue its ParseJob — identical to the PDF path's tail but with the
// pdfcpu/bookmark steps skipped.
func splitEpubSingleShard(ctx context.Context, deps Deps, tx pgx.Tx, doc *store.Document, tmpDir string) error {
	docID := doc.ID
	// Idempotency mirrors the PDF path's GetShard probe above.
	already, err := deps.DB.GetShard(ctx, docID, 0)
	if err != nil {
		return err
	}
	if already != nil {
		if doc.State == store.StateUploaded {
			return deps.DB.SetDocState(ctx, tx, docID, store.StateParsing, nil, nil)
		}
		return nil
	}
	if err := deps.DB.SetDocState(ctx, tx, docID, store.StateParsing, nil, nil); err != nil {
		return err
	}
	if err := updateSplitMeta(ctx, tx, docID, 1, 1); err != nil {
		return err
	}
	if _, err := deps.DB.InsertShards(ctx, tx, docID, [][2]int{{1, 1}}, 0); err != nil {
		return err
	}
	job := queue.ParseJob{
		SchemaVersion: queue.SchemaVersion,
		DocID:         docID,
		Idx:           0,
		PageStart:     1,
		PageEnd:       1,
		SourceURI:     doc.SourceURI,
	}
	if _, err := queue.XAddJob(ctx, deps.Redis, queue.StreamParse, job); err != nil {
		return err
	}
	slog.Info("split complete (epub, single shard)", "doc", docID)
	return nil
}

// updateSplitMeta sets total_shards + page_count in one statement.
func updateSplitMeta(ctx context.Context, tx pgx.Tx, docID string, totalShards, pageCount int) error {
	_, err := tx.Exec(ctx,
		`UPDATE documents SET total_shards = $2, page_count = $3, updated_at = NOW() WHERE id = $1`,
		docID, totalShards, pageCount)
	return err
}

func toBoundPairs(bounds []pipeline.ShardBound) [][2]int {
	out := make([][2]int, len(bounds))
	for i, b := range bounds {
		out[i] = [2]int{b.PageStart, b.PageEnd}
	}
	return out
}

func jobString(job map[string]any, key string) (string, error) {
	v, ok := job[key].(string)
	if !ok || v == "" {
		return "", fmt.Errorf("job field %q missing", key)
	}
	return v, nil
}
