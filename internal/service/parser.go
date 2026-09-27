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
	return !(pageStart == 1 && pageEnd >= totalPages) //nolint:staticcheck,QF1001 // clearer intent than De Morgan form
}

// SplitAndRequeue replaces a failing shard with finer sub-shards (ladder
// attempts 2-3). The parent becomes SKIPPED; each sub-shard is a fresh
// ParseJob. Port of parser.py _split_and_requeue.
//
// R-36 phase split: all DB writes (NextShardIdx/SkipShard/InsertShards/
// total_shards UPDATE/WriteEvent) run inside ONE short TxWithRetry, and the
// XAddJob loop runs strictly AFTER that tx commits — closing the R-32
// follow-up (enqueue-before-commit let a racing parser ClaimShard see 0
// rows and ACK the job into oblivion). The tx fn is idempotent on retry
// (NextShardIdx is computed inside the tx; a retry re-reads it).
func SplitAndRequeue(ctx context.Context, deps Deps, tx pgx.Tx, doc *store.Document, shard *store.Shard) error {
	return resplitShard(ctx, deps, doc, shard)
}

// resplitShard is SplitAndRequeue's body with the R-36 tx shape: one short
// tx for the DB writes, enqueue strictly after the tx returns. The runner's
// tx parameter is unused for writes here (the runner tx commits empty).
func resplitShard(ctx context.Context, deps Deps, doc *store.Document, shard *store.Shard) error {
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
	// Jobs are BUILT before the tx (pure, from data already in hand) and
	// SENT only after the tx commits — the ordering contract the pure seam
	// test pins (TestSubShardJobs).
	startIdx := -1
	var jobs []queue.ParseJob
	err = deps.DB.TxWithRetry(ctx, 3, func(tx pgx.Tx) error {
		var txErr error
		startIdx, txErr = deps.DB.NextShardIdx(ctx, tx, doc.ID)
		if txErr != nil {
			return txErr
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
		return store.WriteEvent(ctx, tx, "warn", "parse",
			fmt.Sprintf("shard %d re-split into %d sub-shards (ladder attempt %d)", shard.Idx, len(bounds), shard.Attempts),
			strPtr(doc.ID), intPtr(shard.Idx), strPtr("SHARD_RESPLIT"), nil, nil)
	})
	if err != nil {
		return err
	}
	jobs = subShardJobs(doc, shard, bounds, startIdx)
	for _, job := range jobs {
		if _, err := queue.XAddJob(ctx, deps.Redis, queue.StreamParse, job); err != nil {
			return err
		}
	}
	return nil
}

// subShardJobs builds one ParseJob per bound at startIdx — the pure enqueue
// payload builder for resplitShard (testable without a DB or Redis; the
// tx/enqueue ordering contract is pinned by building before and sending
// after).
func subShardJobs(doc *store.Document, shard *store.Shard, bounds [][2]int, startIdx int) []queue.ParseJob {
	jobs := make([]queue.ParseJob, 0, len(bounds))
	for i, b := range bounds {
		jobs = append(jobs, queue.ParseJob{
			SchemaVersion: queue.SchemaVersion,
			DocID:         doc.ID,
			Idx:           startIdx + i,
			PageStart:     b[0],
			PageEnd:       b[1],
			SourceURI:     doc.SourceURI,
		})
	}
	return jobs
}

// HandleParse is the parser handler: THE heavy one (PRD §6.3).
//
// R-36 phase split (lock-ordering rule §1.3.5): the runner's tx parameter
// is intentionally left unused for writes — the runner tx commits empty.
// The handler runs as short tx A (claim) → NO tx for the S3/pdfcpu/
// anydoc/docling/S3-upload work → short tx B (MarkShardDone + settled
// re-read) → enqueue AFTER B commits. This removes the shard row lock held
// across the entire parse — the standing lock reservoir behind the
// 2026-09-27 deadlock storm — and makes the embed enqueue strictly
// post-commit (the R-32 follow-up: the embedder can never stitch a doc
// missing the just-committed last shard).
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

	// Phase A: reads (pool, no tx) + claim (short tx). The claim takes the
	// shard row lock; holding it across the parse below would recreate the
	// (A) reservoir, so the claim tx commits immediately.
	doc, err := deps.DB.GetDocument(ctx, docID)
	if err != nil {
		return err
	}
	if doc == nil {
		return errors.NewPlatformError(errors.CodePDFCorrupt, fmt.Sprintf("document %s vanished", docID))
	}

	workerID := fmt.Sprintf("parser-%s", shortID())
	shard, err := claimShard(ctx, deps, docID, idx, workerID, s.ShardLeaseSeconds)
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

	// Phase B: results (short tx) + enqueue AFTER the tx commits. The
	// settle check reads the doc row through the same tx that incremented
	// shards_done (a pool read cannot see the uncommitted increment), and
	// the embed job is XADDed only once that tx has committed — an embedder
	// waking on the XADD always sees the final shard (hazard (E), R-32
	// follow-up).
	if _, err := finishParse(ctx, deps, docID, idx, durationMS, result.PeakRSSMB,
		"s3://"+s.S3BucketParsed+"/"+parsedKey, result.NeedsOCR, floatPtr(result.MeanCharsPerPage)); err != nil {
		return err
	}
	return nil
}

// claimShard is HandleParse's phase A claim: one short TxWithRetry around
// the atomic ClaimShard, committed before any parse work starts. Retrying
// the whole claim is safe — ClaimShard's `state IN ('pending','failed')`
// guard means a retry either re-claims the same row (if the failed tx
// rolled back) or matches 0 rows (someone else got it → nil shard).
func claimShard(ctx context.Context, deps Deps, docID string, idx int,
	workerID string, leaseSeconds int) (*store.Shard, error) {
	var shard *store.Shard
	err := deps.DB.TxWithRetry(ctx, 3, func(tx pgx.Tx) error {
		var txErr error
		shard, txErr = deps.DB.ClaimShard(ctx, tx, docID, idx, workerID, leaseSeconds)
		return txErr
	})
	if err != nil {
		return nil, err
	}
	return shard, nil
}

// finishParse is HandleParse's phase B: MarkShardDone + the settled re-read
// inside ONE short TxWithRetry; the embed enqueue happens strictly AFTER
// the tx returns (R-36 hazard (E) fix — an embedder waking on the XADD
// cannot run before the last shard is committed). Returns BookSettled of
// the freshly-read doc row.
func finishParse(ctx context.Context, deps Deps, docID string, idx, durationMS, peakRSSMB int,
	parsedURI string, needsOCR bool, meanChars *float64) (settled bool, err error) {
	fresh := (*store.Document)(nil)
	err = deps.DB.TxWithRetry(ctx, 3, func(tx pgx.Tx) error {
		if err := deps.DB.MarkShardDone(ctx, tx, docID, idx, durationMS, peakRSSMB,
			parsedURI, needsOCR, meanChars); err != nil {
			return err
		}
		var txErr error
		fresh, txErr = deps.DB.GetDocumentTx(ctx, tx, docID)
		return txErr
	})
	if err != nil {
		return false, err
	}
	settled = settledAfterDone(fresh)
	if settled {
		if _, err := queue.XAddJob(ctx, deps.Redis, queue.StreamEmbed, queue.EmbedJob{
			SchemaVersion: queue.SchemaVersion, DocID: docID,
		}); err != nil {
			return true, err
		}
	}
	return settled, nil
}

func floatPtr(f float64) *float64 { return &f }

// settledAfterDone is finishParse's settle decision as a pure seam — a thin
// wrapper over store.BookSettled kept named here so the table test reads as
// the handler's decision, not the store helper's.
func settledAfterDone(doc *store.Document) bool {
	return doc != nil && store.BookSettled(doc)
}

func copyFileLocal(dst, src string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}
