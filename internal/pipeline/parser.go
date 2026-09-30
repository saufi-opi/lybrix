package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"time"
) // TwoTierParser orchestrates blueprint §4.2's two-tier flow with the 1.0
// retry ladder semantics layered on the caller side:
//
//	OCR gate (cheap, pdfcpu text layer) →
//	born-digital → anydoc in-process (shard-level) →
//	yield check len(md)/pages >= MinYieldCharsPerPage (50) → accept;
//	else scanned/complex → docling-serve whole-shard multipart POST.
type TwoTierParser struct {
	AnyDoc  Parser
	Docling *DoclingClient
	// MinYieldCharsPerPage is the fast-path yield threshold (50).
	MinYieldCharsPerPage int
	// OCRMinCharsPerPage is the text-layer gate threshold (20).
	OCRMinCharsPerPage int
}

// NewTwoTierParser wires the tier selection. anydoc availability is
// compile-time (stub vs CGO); ANYDOC_ENABLED=false forces the fallback
// regardless.
func NewTwoTierParser(doclingURL string, minYield, ocrMin int, anydocEnabled bool) *TwoTierParser {
	var anydoc Parser = AnyDocParser{}
	if !anydocEnabled || !anydoc.Available() {
		anydoc = unavailableParser{}
	}
	return &TwoTierParser{
		AnyDoc:               anydoc,
		Docling:              NewDoclingClient(doclingURL),
		MinYieldCharsPerPage: minYield,
		OCRMinCharsPerPage:   ocrMin,
	}
}

// unavailableParser makes the "no fast path" choice explicit.
type unavailableParser struct{}

func (unavailableParser) Parse(context.Context, ParseRequest) (ParseResult, error) {
	return ParseResult{}, ErrAnyDocUnavailable
}
func (unavailableParser) Available() bool { return false }
func (unavailableParser) Supports(Format) bool {
	return false
}

// anydocEligible is the tier-1 gate: the fast path is attempted only when
// the parser is linked, the shard is born-digital, and the parser itself
// claims the format (capability-driven; the stub claims nothing).
func (t *TwoTierParser) anydocEligible(req ParseRequest) bool {
	return !req.NeedOCR && t.AnyDoc.Available() && t.AnyDoc.Supports(req.DocFormat)
}

// Parse runs the two-tier flow for one shard.
//
// Routing (Workstream 2, capability-driven): the tier-1 gate asks the
// linked parser whether it supports the shard's format — the binding's
// capability decides routing, not a hand-maintained table. PDF keeps the
// full gate → anydoc → yield → docling flow. EPUB now attempts anydoc
// first too (the binding carries ANYDOC_FORMAT_EPUB) with docling as the
// error/low-yield fallback. DOCX/PPTX/XLSX skip the OCR gate (no pdfcpu
// page probe on a non-PDF) and take the anydoc fast path with a docling
// fallback. TXT/MD are pure-Go passthrough (file bytes are the markdown —
// no CGO, so the CI stub lane works). HTML goes to docling directly (the
// binding has no html mapping). Non-PDF shards are single synthetic
// shards — the file passed whole.
func (t *TwoTierParser) Parse(ctx context.Context, req ParseRequest) (ParseResult, error) {
	started := time.Now()
	pages := req.PageEnd - req.PageStart + 1
	if pages < 1 {
		pages = 1
	}

	// Gate first when the caller didn't already run it: scanned shards skip
	// the anydoc attempt entirely (ocr_gate before Docling was 1.0's cheap
	// ordering, and the blueprint keeps OCR off for the ~90% with a text
	// layer). Only PDF shards can run the gate — the pdfcpu text-layer
	// probe cannot open zip or text containers.
	needOCR := req.NeedOCR
	var meanChars float64
	if req.DocFormat == FmtPDF && !needOCR && req.PDFPath != "" {
		verdict, err := NeedsOCR(req.PDFPath, req.PageStart, req.PageEnd, t.OCRMinCharsPerPage)
		if err == nil {
			needOCR = verdict.NeedsOCR
			meanChars = verdict.MeanCharsPerPage
		}
	}

	// TXT/MD passthrough (tier 0): the file bytes ARE the markdown. Empty
	// output falls through to docling below.
	if req.DocFormat == FmtTXT || req.DocFormat == FmtMD {
		if md, err := os.ReadFile(req.PDFPath); err == nil && YieldOK(string(md), pages, t.MinYieldCharsPerPage) {
			return ParseResult{
				Markdown:         string(md),
				NeedsOCR:         false,
				MeanCharsPerPage: meanChars,
				DurationMS:       time.Since(started).Milliseconds(),
				PeakRSSMB:        CurrentRSSMB(),
				Engine:           "text",
			}, nil
		}
		// empty / unreadable → docling fallback below (it accepts txt/md)
		md, err := t.Docling.ConvertNamed(ctx, req.PDFPath, ParseSourceName(req.DocFormat), !req.TextOnly)
		if err != nil {
			return ParseResult{}, err
		}
		return ParseResult{
			Markdown:         md,
			NeedsOCR:         needOCR,
			MeanCharsPerPage: meanChars,
			DurationMS:       time.Since(started).Milliseconds(),
			PeakRSSMB:        CurrentRSSMB(),
			Engine:           "docling",
		}, nil
	}

	// HTML: anydoc has no html code — docling-serve directly.
	if req.DocFormat == FmtHTML {
		md, err := t.Docling.ConvertNamed(ctx, req.PDFPath, ParseSourceName(FmtHTML), !req.TextOnly)
		if err != nil {
			return ParseResult{}, err
		}
		return ParseResult{
			Markdown:         md,
			NeedsOCR:         needOCR,
			MeanCharsPerPage: meanChars,
			DurationMS:       time.Since(started).Milliseconds(),
			PeakRSSMB:        CurrentRSSMB(),
			Engine:           "docling",
		}, nil
	}

	// Tier 1: anydoc in-process — capability-driven: the gate consults the
	// parser itself (EPUB included since the binding carries
	// ANYDOC_FORMAT_EPUB = 7); born-digital only.
	if t.anydocEligible(req) {
		res, err := t.AnyDoc.Parse(ctx, req)
		if err == nil && YieldOK(res.Markdown, pages, t.MinYieldCharsPerPage) {
			res.NeedsOCR = false
			res.MeanCharsPerPage = meanChars
			if res.DurationMS == 0 {
				res.DurationMS = time.Since(started).Milliseconds()
			}
			return res, nil
		}
		if err != nil && !isUnavailable(err) {
			slog.Debug("anydoc attempt failed; falling back to docling",
				"err", err, "pages", fmt.Sprintf("%d-%d", req.PageStart, req.PageEnd))
		}
	}

	// Tier 2: docling-serve whole-shard fallback. Retry ladder ≥4 runs
	// text-only (table structure off). Every non-PDF shard carries its own
	// source filename so docling-serve's format sniffing sees the extension.
	if req.IsSynthetic() {
		md, err := t.Docling.ConvertNamed(ctx, req.PDFPath, ParseSourceName(req.DocFormat), !req.TextOnly)
		if err != nil {
			return ParseResult{}, err
		}
		return ParseResult{
			Markdown:         md,
			NeedsOCR:         needOCR,
			MeanCharsPerPage: meanChars,
			DurationMS:       time.Since(started).Milliseconds(),
			PeakRSSMB:        CurrentRSSMB(),
			Engine:           "docling",
		}, nil
	}
	md, err := t.Docling.Convert(ctx, req.PDFPath, !req.TextOnly)
	if err != nil {
		return ParseResult{}, err
	}
	return ParseResult{
		Markdown:         md,
		NeedsOCR:         needOCR,
		MeanCharsPerPage: meanChars,
		DurationMS:       time.Since(started).Milliseconds(),
		PeakRSSMB:        CurrentRSSMB(),
		Engine:           "docling",
	}, nil
}

func isUnavailable(err error) bool {
	return errors.Is(err, ErrAnyDocUnavailable)
}

// CurrentRSSMB reports peak RSS in MB — the 1.0 memory.py current_rss_mb
// port. Go GC makes the soft-OOM self-check moot, but the recording stays
// (the shard row carries peak_rss_mb and the janitor rollup charts it).
func CurrentRSSMB() int {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return int(ms.Sys / (1024 * 1024))
}

// PDFCachePath is the host-local cache path for a doc's source PDF.
func PDFCachePath(cacheDir, docID string) string {
	return filepath.Join(cacheDir, docID+".pdf")
}

// StorePDFCache atomically writes a downloaded PDF into the host cache:
// tmp file in the same dir, then os.replace. Concurrent shard jobs of the
// same book may race — os.replace is atomic, losers just overwrite with
// identical bytes. The cache is a pure optimization — never fail the shard
// over it.
func StorePDFCache(cacheDir, docID, srcPath string) {
	if cacheDir == "" {
		return
	}
	// The cache is a pure optimization — never fail the shard over it.
	// MkdirAll guards against a vanished/recreated mountpoint between
	// jobs (volume provisioned lazily, container restarts) — ENOENT on
	// the tmp write otherwise poisons every job of the doc (R-35).
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		slog.Warn("pdf cache mkdir failed (ignored)", "err", err, "dir", cacheDir)
		return
	}
	dst := PDFCachePath(cacheDir, docID)
	tmp := dst + ".tmp"
	if err := copyFile(tmp, srcPath); err != nil { // copyFile(dst, src): write the .tmp, read the source (R-35: args were swapped)
		slog.Warn("pdf cache store failed (ignored)", "err", err, "dst", tmp, "src", srcPath)
		return
	}
	if err := os.Rename(tmp, dst); err != nil {
		slog.Warn("pdf cache store failed (ignored)", "err", err, "tmp", tmp, "dst", dst)
		_ = os.Remove(tmp)
	}
}

func copyFile(dst, src string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}
