package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/saufi-opi/lybrix/internal/errors"
	"github.com/saufi-opi/lybrix/internal/objectstore"
	"github.com/saufi-opi/lybrix/internal/pipeline"
	"github.com/saufi-opi/lybrix/internal/store"
)

// embedMaxAttempts: terminal-failure cap (2026-09-11 incident): a job whose
// embed kept failing was retried forever. Failures bump a per-doc Redis
// counter; on reaching the cap the doc is marked FAILED (terminal) and the
// handler returns so the runner ACKs. Success resets the counter.
const embedMaxAttempts = 5

// prefetchWorkers is the parallel S3 fan-out for shard JSONs (R-9).
const prefetchWorkers = 8

// HandleEmbed is the embedder handler (PRD §6.4):
//
//	load done shards (page_start order) → parallel S3 prefetch (8 workers) →
//	stitch (boundary-heading dedupe + per-line page map) → ChunkHierarchical →
//	insert parents + children (ON CONFLICT DO NOTHING) → embed children in
//	batches → doc ready/partial + completeness + chunk_count.
func HandleEmbed(ctx context.Context, deps Deps, tx pgx.Tx, job map[string]any) error {
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

	shardRows, err := deps.DB.SettledShards(ctx, docID)
	if err != nil {
		return err
	}
	if len(shardRows) == 0 {
		return errors.NewPlatformError(errors.CodePDFCorrupt, "no parsed shards to embed")
	}

	// 1-based inclusive shard page ranges, aligned with the page_start-sorted
	// shard rows. Failed shards have no JSON so they are absent from both
	// sides — ranges stay aligned.
	fetch := func(ctx context.Context, idx int) (string, error) {
		return deps.S3.GetText(ctx, s.S3BucketParsed, objectstore.ParsedKey(docID, idx))
	}
	keys := make([]int, 0, len(shardRows))
	ranges := map[int][2]int{}
	for _, sh := range shardRows {
		keys = append(keys, sh.Idx)
		ranges[sh.Idx] = [2]int{sh.PageStart, sh.PageEnd}
	}
	fetched, err := pipeline.Prefetch(ctx, keys, fetch, prefetchWorkers)
	if err != nil {
		return err
	}

	stitched := pipeline.Stitch(fetched, ranges)
	parents, children := pipeline.ChunkHierarchical(stitched.Markdown, stitched.Pages, pipeline.WhitespaceTokenizer{})
	if len(children) == 0 && len(parents) == 0 {
		return errors.NewPlatformError(errors.CodePDFCorrupt, "stitch/chunk produced no chunks")
	}
	// Document-wide dedupe (BACKLOG R-24): keep the first occurrence of each
	// hash across the whole doc (order preserved, seq renumbered). Non-
	// adjacent repeats — boilerplate, footers, page furniture stitched across
	// shard boundaries — previously reached COPY and aborted the tx.
	parents = pipeline.DropDuplicateChunks(parents,
		func(p pipeline.ParentChunk) string { return p.ChunkHash },
		func(p *pipeline.ParentChunk, seq int) { p.Seq = seq })
	children = pipeline.DropDuplicateChunks(children,
		func(c pipeline.ChildChunk) string { return c.ChunkHash },
		func(c *pipeline.ChildChunk, seq int) { c.Seq = seq })

	// Cross-set dedupe, parent wins: chunk_hash is HashText over unprefixed
	// text and the row id is derived from (doc_id, hash) with no is_parent
	// component, so a parent and child with identical text would collide on
	// chunks_pkey (a heading-less short section: parent text == child window
	// text). Parents insert first, so the child is dropped here — before
	// texts/chunkIDs are built — rather than being silently dropped by the
	// insert fallback and having its vector written onto the parent row.
	// (After parent dedupe children's ParentSeq values go stale; there is no
	// consumer — LinkParentsByHash joins by seq proximity and dedupe
	// preserves relative order, so its semantics survive.)
	pHashes := make(map[string]struct{}, len(parents))
	for _, p := range parents {
		pHashes[p.ChunkHash] = struct{}{}
	}
	kept := children[:0] // in-place filter is safe: drops only, order preserved
	nKept := 0
	for _, c := range children {
		if _, dup := pHashes[c.ChunkHash]; dup {
			continue
		}
		c.Seq = nKept
		nKept++
		kept = append(kept, c)
	}
	children = kept
	if len(children) == 0 && len(parents) == 0 {
		return errors.NewPlatformError(errors.CodePDFCorrupt, "stitch/chunk produced no chunks")
	}

	// Idempotent insert: UNIQUE(doc_id, chunk_hash) → ON CONFLICT DO NOTHING.
	if err := deps.DB.InsertParents(ctx, tx, docID, docCollection(doc), toStoreParents(parents)); err != nil {
		return err
	}
	if err := deps.DB.InsertChunks(ctx, tx, docID, docCollection(doc), toStoreChildren(children)); err != nil {
		return err
	}
	if err := deps.DB.LinkParentsByHash(ctx, tx, docID); err != nil {
		return err
	}

	// Embed children against the collection's bound model on the ingest
	// plane in moderate batches; the backend's dynamic batcher packs them —
	// our job is a steady stream of moderate requests (§6.4).
	m, err := deps.DB.ResolveCollectionModel(ctx, docCollection(doc))
	if err != nil {
		return err
	}
	if m == nil {
		return errors.NewPlatformError(errors.CodeEmbedDimMismatch, "no embedding model configured")
	}
	client, err := pipeline.NewEmbedClient(pipeline.EmbedSpec{
		Provider:      m.Provider,
		ModelID:       m.ModelID,
		IngestURL:     m.IngestURL,
		QueryURL:      m.QueryURL,
		TruncateChars: m.TruncateChars,
	}, "ingest")
	if err != nil {
		return err
	}
	texts := make([]string, 0, len(children))
	// Child text passed to the embedder is breadcrumb-prefixed
	// ("[Doc > Chapter > Section] " + text) per blueprint §5; stored text
	// stays unprefixed.
	for _, c := range children {
		if c.HeaderBreadcrumb != "" {
			texts = append(texts, strings.Replace(c.HeaderBreadcrumb, "[ ", "[", 1)+c.Text)
		} else {
			texts = append(texts, c.Text)
		}
	}
	batches, _ := pipeline.PlanBatches(texts, pipeline.WhitespaceTokenizer{}, m.CtxBudget, m.BatchSize)
	chunkIDs := make([]string, len(children))
	// child ids must line up with texts: ids are deterministic UUIDv5 from
	// (doc_id, chunk_hash) — the same derivation InsertChunks used, so the
	// embed step never needs a DB round-trip to find what it inserted.
	for i, c := range children {
		chunkIDs[i] = deterministicUUID(docID, c.ChunkHash)
	}
	embedIdx := 0
	for _, batch := range batches {
		vectors, err := client.Embed(ctx, batch, m.VectorDim)
		if err != nil {
			return capped(ctx, deps, tx, docID, err)
		}
		for j, vec := range vectors {
			if embedIdx+j < len(chunkIDs) {
				if err := deps.DB.UpdateEmbeddingTx(ctx, tx, chunkIDs[embedIdx+j], vec); err != nil {
					return err
				}
			}
		}
		embedIdx += len(batch)
	}

	if err := deps.DB.SetChunkCountAndCompleteness(ctx, tx, docID, len(children)); err != nil {
		return err
	}
	if err := resetEmbedRetries(ctx, deps, docID); err != nil {
		slog.Warn("embed retry counter reset failed (ignored)", "err", err)
	}
	slog.Info("embed done", "doc", docID, "chunks", len(children), "parents", len(parents))
	return nil
}

// capped counts a failure; when the cap is reached mark the doc FAILED and
// return nil (handler must return → runner ACKs → retry loop ends).
func capped(ctx context.Context, deps Deps, tx pgx.Tx, docID string, cause error) error {
	attempts, err := bumpEmbedRetries(ctx, deps, docID)
	if err != nil {
		attempts = 1 // fail-safe: treat as first failure
	}
	if attempts < embedMaxAttempts {
		slog.Warn("embed failed (attempt)", "attempt", attempts, "max", embedMaxAttempts, "doc", docID, "err", cause)
		return cause
	}
	slog.Error("embed cap reached — marking FAILED", "attempts", attempts, "doc", docID)
	detail := fmt.Sprintf("embed failed %dx: %v", attempts, cause)
	return deps.DB.SetDocState(ctx, tx, docID, store.StateFailed,
		strPtr(string(errors.CodeDocEmbedFailed)), strPtr(detail))
}

// bumpEmbedRetries increments the per-doc embed failure counter. The
// counter carries a 6h expiry: isolated failures hours apart must not
// accumulate into a false cap (an infra outage should not permanently
// poison a doc), while a tight pathological loop still reaches the cap
// within minutes.
func bumpEmbedRetries(ctx context.Context, deps Deps, docID string) (int, error) {
	key := "embed:retries:" + docID
	n, err := deps.Redis.Incr(ctx, key).Result()
	if err != nil {
		return 1, err
	}
	deps.Redis.Expire(ctx, key, 6*3600*time.Second)
	return int(n), nil
}

// resetEmbedRetries deletes the counter on success.
func resetEmbedRetries(ctx context.Context, deps Deps, docID string) error {
	return deps.Redis.Del(ctx, "embed:retries:"+docID).Err()
}
