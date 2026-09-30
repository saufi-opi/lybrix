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
		// no doc row — nothing to mark terminal and events.doc_id carries an
		// FK, so log only (R-39); nil ACKs and ends the loop.
		slog.Warn("split for vanished doc row — acking no-op", "doc", docID)
		return nil
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
		if objectstore.IsNotFound(err) {
			// the raw object is genuinely gone — terminal (R-39); a transient
			// S3 blip stays retryable
			return failDocTerminal(ctx, deps, docID, errors.CodePDFCorrupt,
				fmt.Sprintf("raw source object vanished: %v", err))
		}
		return errors.NewPlatformError(errors.CodePDFCorrupt, fmt.Sprintf("source missing: %v", err))
	}

	// Non-PDF branch (EPUB / office / text / HTML — Workstream 2): pdfcpu
	// would raise PDF_CORRUPT on a zip container (and cannot probe text
	// files at all), so every non-PDF path skips PageCount/ExtractBookmarks
	// entirely and emits exactly ONE synthetic shard (idx=0, pages 1-1) —
	// the parser routes it by DocFormat (docling parses EPUB/HTML natively,
	// office docs take the anydoc fast path or docling, text passes
	// through), and treats the whole file as one "page".
	if doc.MimeType == nil || *doc.MimeType != pipeline.FmtPDF.MimeType() {
		return splitSingleShard(ctx, deps, tx, doc, tmpDir)
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

	pairs := toBoundPairs(bounds)
	if err := commitSplit(ctx, deps, docID, pairs, pageCount); err != nil {
		return err
	}
	// Parse jobs are enqueued only after the shard rows are committed
	// (R-32): an idle parser's ClaimShard sees 0 rows inside the runner's
	// uncommitted tx, treats that as "someone else got it", and ACKs —
	// destroying the job (XACK+XDEL). A crash between commit and enqueue is
	// healed by the janitor's pendingShardSweep (R-32), never by re-enqueue
	// on retry — the retry takes the idempotent branch above.
	for _, job := range parseJobsFor(docID, doc.SourceURI, pairs) {
		if _, err := queue.XAddJob(ctx, deps.Redis, queue.StreamParse, job); err != nil {
			return err
		}
	}
	slog.Info("split complete", "doc", docID, "pages", pageCount, "shards", len(bounds))
	return nil
}

// splitSingleShard is the non-PDF splitter tail: advance UPLOADED →
// PARSING, write one synthetic shard (idx=0, page_start=1, page_end=1),
// enqueue its ParseJob — identical to the PDF path's tail but with the
// pdfcpu/bookmark steps skipped. Like the PDF path, the DB writes commit in
// an inner transaction and the enqueue happens only afterwards (R-32).
func splitSingleShard(ctx context.Context, deps Deps, tx pgx.Tx, doc *store.Document, tmpDir string) error {
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
	if err := commitSplit(ctx, deps, docID, [][2]int{{1, 1}}, 1); err != nil {
		return err
	}
	// Same R-32 invariant as the PDF path: enqueue only after commit.
	for _, job := range parseJobsFor(docID, doc.SourceURI, [][2]int{{1, 1}}) {
		if _, err := queue.XAddJob(ctx, deps.Redis, queue.StreamParse, job); err != nil {
			return err
		}
	}
	slog.Info("split complete (single synthetic shard)", "doc", docID)
	return nil
}

// commitSplit performs the split's DB tail — UPLOADED → PARSING, split
// metadata, one shard row per bound — inside its OWN transaction
// (deps.DB.Tx), not the runner's. The runner's outer tx then commits
// empty. This is the R-32 fix: ParseJobs must XADD only after these rows
// are visible to other transactions, or an idle parser claims nothing,
// acks the job, and the shard is stranded pending attempts=0 forever.
func commitSplit(ctx context.Context, deps Deps, docID string, bounds [][2]int, pageCount int) error {
	return deps.DB.Tx(ctx, func(tx pgx.Tx) error {
		if err := deps.DB.SetDocState(ctx, tx, docID, store.StateParsing, nil, nil); err != nil {
			return err
		}
		if err := updateSplitMeta(ctx, tx, docID, len(bounds), pageCount); err != nil {
			return err
		}
		_, err := deps.DB.InsertShards(ctx, tx, docID, bounds, 0)
		return err
	})
}

// parseJobsFor builds one ParseJob per bound — pure, so the enqueue payload
// is testable without a DB.
func parseJobsFor(docID, sourceURI string, bounds [][2]int) []queue.ParseJob {
	jobs := make([]queue.ParseJob, len(bounds))
	for i, b := range bounds {
		jobs[i] = queue.ParseJob{
			SchemaVersion: queue.SchemaVersion,
			DocID:         docID,
			Idx:           i,
			PageStart:     b[0],
			PageEnd:       b[1],
			SourceURI:     sourceURI,
		}
	}
	return jobs
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
