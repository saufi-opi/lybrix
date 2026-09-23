package service

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/saufi-opi/lybrix/internal/errors"
	"github.com/saufi-opi/lybrix/internal/objectstore"
	"github.com/saufi-opi/lybrix/internal/pipeline"
	"github.com/saufi-opi/lybrix/internal/queue"
	"github.com/saufi-opi/lybrix/internal/store"
)

// ladderAction decides the retry ladder step (parser.py _ladder_action):
// attempt 2: quarter-split; attempt 3: single-page; attempt >=4: text-only.
// A shard too small to split skips straight to text-only.
// Returns "split"|"text_only".
//
// EPUB rule (PLAN.md item 19): a span-1 shard (every EPUB synthetic shard)
// already fails both split conditions and falls through to text_only —
// retries re-attempt the same single shard with docling backoff, and no
// duplicate sub-shards are ever created. The explicit span-1 guard below
// pins that contract against future threshold drift.
func LadderAction(shard *store.Shard, shardPages int) string {
	span := shard.PageEnd - shard.PageStart + 1
	if span < 2 {
		return "text_only" // single-page/EPUB synthetic shard: never split
	}
	if shard.Attempts == 2 && span >= 4*(maxInt(1, shardPages/4))/2 { // worth quarter-splitting
		return "split"
	}
	if shard.Attempts == 3 && span >= 2 {
		return "split" // single-page sub-shards
	}
	return "text_only"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// shouldSlicePDF decides whether HandleParse cuts the shard's page range out
// of the source PDF before parsing (BACKLOG R-22). Guards, in order:
//
//   - only real PDF shards slice — non-PDF shards are synthetic single
//     shards, the whole file is their unit;
//   - both range bounds must be known (0 means "no range in the job") and
//     well-ordered (pageEnd >= pageStart, matching ParseJob.ValidatePages'
//     invariant);
//   - a shard spanning the entire document is passed through untouched
//     (no pointless copy).
func shouldSlicePDF(docFormat pipeline.Format, pageStart, pageEnd, totalPages int) bool {
	if docFormat != pipeline.FmtPDF {
		return false
	}
	if pageStart <= 0 || pageEnd < pageStart {
		return false
	}
	return !(pageStart == 1 && pageEnd >= totalPages)
}

// SplitAndRequeue replaces a failing shard with finer sub-shards (ladder
// attempts 2-3). The parent becomes SKIPPED; each sub-shard is a fresh
// ParseJob. Port of parser.py _split_and_requeue.
func SplitAndRequeue(ctx context.Context, deps Deps, tx pgx.Tx, doc *store.Document, shard *store.Shard) error {
	span := shard.PageEnd - shard.PageStart + 1
	shardPages := 1
	if shard.Attempts < 3 {
		shardPages = maxInt(1, span/4)
	}
	fb, err := pipeline.FixedBounds(span, shardPages, 0)
	if err != nil {
		return err
	}
	// overlap=0 is required: sub-shards must tile the parent disjointly
	// (fixed_bounds' default overlap=1 would emit overlapping bounds —
	// a span-20 attempt-2 shard would yield 5 overlapping bounds like
	// [1-5],[5-9],[9-13],... instead of 4 disjoint ones, double-counting
	// total_shards and re-parsing boundary pages).
	bounds := make([][2]int, 0, len(fb))
	for _, b := range fb {
		bounds = append(bounds, [2]int{shard.PageStart + b.PageStart - 1, shard.PageStart + b.PageEnd - 1})
	}
	startIdx, err := deps.DB.NextShardIdx(ctx, tx, doc.ID)
	if err != nil {
		return err
	}
	if err := deps.DB.SkipShard(ctx, tx, doc.ID, shard.Idx); err != nil {
		return err
	}
	if _, err := deps.DB.InsertShards(ctx, tx, doc.ID, bounds, startIdx); err != nil {
		return err
	}
	newTotal := (derefInt(doc.TotalShards)) + len(bounds)
	if _, err := tx.Exec(ctx,
		`UPDATE documents SET total_shards = $2, updated_at = NOW() WHERE id = $1`, doc.ID, newTotal); err != nil {
		return err
	}
	for i, b := range bounds {
		job := queue.ParseJob{
			SchemaVersion: queue.SchemaVersion,
			DocID:         doc.ID,
			Idx:           startIdx + i,
			PageStart:     b[0],
			PageEnd:       b[1],
			SourceURI:     doc.SourceURI,
		}
		if _, err := queue.XAddJob(ctx, deps.Redis, queue.StreamParse, job); err != nil {
			return err
		}
	}
	return store.WriteEvent(ctx, tx, "warn", "parse",
		fmt.Sprintf("shard %d re-split into %d sub-shards (ladder attempt %d)", shard.Idx, len(bounds), shard.Attempts),
		strPtr(doc.ID), intPtr(shard.Idx), strPtr("SHARD_RESPLIT"), nil, nil)
}

// HandleParse is the parser handler: THE heavy one (PRD §6.3).
//
// Memory discipline, all five controls:
//
//	one job per process (prefetch=1)        — compose env + runner wiring
//	process recycling (PARSER_RECYCLE_AFTER) — compose + runner
//	tmpfs cap                                — compose
//	retry ladder                             — below
//	soft RSS (no-op in Go; recording stays)  — pipeline.CurrentRSSMB
func HandleParse(ctx context.Context, deps Deps, tx pgx.Tx, job map[string]any) error {
	s := deps.Settings
	docID, err := jobString(job, "doc_id")
	if err != nil {
		return err
	}
	idx, ok := jobInt(job, "idx")
	pageStart, _ := jobInt(job, "page_start")
	pageEnd, _ := jobInt(job, "page_end")
	if !ok {
		return fmt.Errorf("job field %q missing", "idx")
	}

	doc, err := deps.DB.GetDocument(ctx, docID)
	if err != nil {
		return err
	}
	if doc == nil {
		return errors.NewPlatformError(errors.CodePDFCorrupt, fmt.Sprintf("document %s vanished", docID))
	}

	workerID := fmt.Sprintf("parser-%s", shortID())
	shard, err := deps.DB.ClaimShard(ctx, tx, docID, idx, workerID, s.ShardLeaseSeconds)
	if err != nil {
		return err
	}
	if shard == nil {
		return nil // someone else got it (§6.3 step 1)
	}

	// Retry ladder (PRD §6.3) — attempts are not identical. claim_shard
	// already incremented attempts, so shard.attempts IS this attempt's
	// number.
	textOnly := false
	if shard.Attempts >= 2 {
		action := LadderAction(shard, s.ShardPages)
		if action == "split" {
			return SplitAndRequeue(ctx, deps, tx, doc, shard)
		}
		// "text_only": fall through with a degraded converter config
		textOnly = shard.Attempts >= 4
	}

	tmpDir, err := os.MkdirTemp("", "parse-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	// DocFormat from the doc's mime type — the shared format table is the
	// single source of truth (Workstream 2). Unknown mime → PDF behavior.
	docFormat := pipeline.FormatFromMime(derefStr(doc.MimeType))
	sourceName := pipeline.ParseSourceName(docFormat)
	sourcePath := filepath.Join(tmpDir, sourceName)
	// PDF cache path: bypassed for non-PDF docs (office/text docs are
	// ≤64 MiB — always download; the cache dir holds .pdf files and the
	// cache path derivation is doc-scoped, not mime-scoped).
	cachePath := ""
	if s.ParserPDFCacheDir != "" && docFormat == pipeline.FmtPDF {
		cachePath = pipeline.PDFCachePath(s.ParserPDFCacheDir, docID)
	}
	if cachePath != "" {
		if _, err := os.Stat(cachePath); err == nil {
			// Cache hit — PDFs are immutable per doc_id, no download needed.
			// Copy (not hardlink/ln): tmpfs unlinking on recycle must never
			// touch the cache.
			if err := copyFileLocal(sourcePath, cachePath); err != nil {
				return err
			}
			slog.Info("pdf cache HIT", "doc", docID)
		} else {
			if err := deps.S3.DownloadTo(ctx, s.S3BucketRaw, objectstore.RawKey(docID), sourcePath); err != nil {
				return errors.NewPlatformError(errors.CodePDFCorrupt, fmt.Sprintf("source missing: %v", err))
			}
			pipeline.StorePDFCache(s.ParserPDFCacheDir, docID, sourcePath)
			slog.Info("pdf cache MISS -> STORED", "doc", docID)
		}
	} else {
		if err := deps.S3.DownloadTo(ctx, s.S3BucketRaw, objectstore.RawKey(docID), sourcePath); err != nil {
			return errors.NewPlatformError(errors.CodePDFCorrupt, fmt.Sprintf("source missing: %v", err))
		}
	}

	// Shard slicing (BACKLOG R-22): ParseRequest's page range is consumed by
	// NO parser — docling and anydoc both convert whatever file they are
	// handed end to end. For PDF shards that are not the whole document, cut
	// pageStart..pageEnd out with pdfcpu first so each shard converts only
	// its own pages. Non-PDF shards are synthetic single shards; the whole
	// file is the unit (types.go IsSynthetic).
	parsePath := sourcePath
	reqStart, reqEnd := pageStart, pageEnd
	if docFormat == pipeline.FmtPDF && pageStart > 0 && pageEnd >= pageStart {
		totalPages, perr := pipeline.PageCount(sourcePath)
		if perr != nil {
			// PageCount failure = the PDF may be corrupt/encrypted; leave the
			// request untouched and let the existing conversion/error paths
			// classify it.
			slog.Debug("page count failed; skipping slice", "err", perr, "doc", docID)
		} else if shouldSlicePDF(docFormat, pageStart, pageEnd, totalPages) {
			sliced := filepath.Join(tmpDir, fmt.Sprintf("shard-%d.pdf", idx))
			n, serr := pipeline.SlicePDF(sourcePath, sliced, pageStart, pageEnd)
			if serr == nil && n > 0 {
				parsePath = sliced
				// Renormalize: the slice IS pages 1..n now. The OCR gate inside
				// TwoTierParser probes req.PDFPath at req.PageStart..req.PageEnd,
				// so it must probe the slice.
				reqStart, reqEnd = 1, n
				slog.Info("pdf shard sliced",
					"doc", docID, "shard", idx,
					"pages", fmt.Sprintf("%d-%d/%d", pageStart, pageEnd, totalPages))
			} else if serr != nil {
				// Slice failure must not fail the shard — degrade to the 1.0
				// behavior (whole-file conversion) and let the ladder handle a
				// repeat failure.
				slog.Warn("pdf slice failed; parsing whole file", "err", serr,
					"doc", docID, "shard", idx)
			}
		}
	}

	started := time.Now()
	result, err := deps.Parser.Parse(ctx, pipeline.ParseRequest{
		PDFPath:    parsePath,
		PageStart:  reqStart,
		PageEnd:    reqEnd,
		Attempt:    shard.Attempts,
		ShardPages: s.ShardPages,
		TextOnly:   textOnly,
		SkipAnyDoc: docFormat == pipeline.FmtEPUB,
		DocFormat:  docFormat,
	})
	if err != nil {
		return err
	}
	slog.Info("parse done",
		"doc", docID, "shard", idx, "pages", fmt.Sprintf("%d-%d", pageStart, pageEnd),
		"engine", result.Engine, "mean_chars/page", result.MeanCharsPerPage,
		"needs_ocr", result.NeedsOCR, "duration_ms", result.DurationMS)

	parsedKey := objectstore.ParsedKey(docID, idx)
	if err := deps.S3.UploadText(ctx, s.S3BucketParsed, parsedKey, result.Markdown, "text/markdown"); err != nil {
		return err
	}
	durationMS := int(time.Since(started).Milliseconds())
	if result.DurationMS > 0 {
		durationMS = int(result.DurationMS)
	}
	if err := deps.DB.MarkShardDone(ctx, tx, docID, idx, durationMS, result.PeakRSSMB,
		"s3://"+s.S3BucketParsed+"/"+parsedKey, result.NeedsOCR, floatPtr(result.MeanCharsPerPage)); err != nil {
		return err
	}

	// last shard settled → enqueue embed (§6.3 step 7). MarkShardDone
	// increments shards_done via SQL; re-read the doc row through the same tx
	// before checking — a pool read cannot see the uncommitted increment, so
	// the final shard would appear missing and embedding would wait for the
	// janitor's next pass.
	fresh, err := deps.DB.GetDocumentTx(ctx, tx, docID)
	if err != nil {
		return err
	}
	if fresh != nil && store.BookSettled(fresh) {
		_, err := queue.XAddJob(ctx, deps.Redis, queue.StreamEmbed, queue.EmbedJob{
			SchemaVersion: queue.SchemaVersion, DocID: docID,
		})
		return err
	}
	return nil
}

func floatPtr(f float64) *float64 { return &f }

func copyFileLocal(dst, src string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}
