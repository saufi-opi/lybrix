# BACKLOG — incident & bug ledger

Known-bug memory for the queue/pipeline layer. One row per incident or
confirmed gap: symptom first, root cause second, fix commit last. New
incidents land here before any code change.

| id | priority | status | symptom | root cause | fix commit |
|----|----------|--------|---------|-----------|------------|
| R-1 | P0 | fixed 2026-09-12 (fbcbf59) | janitor flooded doc.embed (~3.2k dup jobs) | settled-book sweep had no dedup vs undelivered jobs | fbcbf59 |
| R-2 | P0 | fixed 2026-09-12 (fbcbf59) | embed job retried forever on Qdrant timeout, doc stuck parsing | no terminal retry cap on embed failures | fbcbf59 |
| R-3 | P1 | fixed 2026-09-12 (07ad045, ea1614c) | dashboard waiting=1 for 73-job backlog (×2 endpoints) | XINFO lag=None after XDEL treated as 0 | 07ad045/ea1614c |
| R-4 | P1 | fixed 2026-09-12 (549e10e) | doc.parse 25,073 entries / 15,994 shards (36% dupes) | reclaim re-added ACKed PEL entries; nothing trims streams | 549e10e |
| R-5 | P1 | fixed 2026-09-12 (acc460e/55658b4) | dashboard 'in-flight' card wrong granularity | doc-count read from ?limit=200 list | acc460e/55658b4 |
| R-6 | P1 | fixed 2026-09-12 (undelivered-dedup scan on doc.split requeue; scan failure skips the requeue) | doc.split flood risk: janitor re-XADDs split job for every UPLOADED doc every pass | no undelivered-dedup on requeue step (janitor.py:141-147); same class as R-1 | this commit |
| R-7 | P2 | fixed 2026-09-12 (job-boundary clean exit in run_consumer: after N successful ACKs the consumer returns; docker restart policy revives it; 0 disables) | parser memory grows unbounded over days | PARSER_RECYCLE_AFTER configured in compose, never implemented in parser.py | this commit |
| R-8 | P2 | fixed 2026-09-12 (janitor reclaim reads per-entry times_delivered via XPENDING; at/over DELIVERY_CAP=max(5, max_shard_attempts+1) the entry is quarantined — DLQ events row + XACK/XDEL — instead of re-added; XPENDING failure/empty answer fails open to re-add) | vanished-doc parse jobs redeliver forever | runner leaves failed entries unacked in the PEL; reclaim re-adds them discarding the delivery count, so a poison job loops deliver→raise→reclaim unbounded (runner docstring promised a DLQ that was never implemented) | this commit |
| R-9 | P3 | fixed 2026-09-12 (parallel prefetch in _prefetch_shards: ThreadPoolExecutor(8), identical failure semantics, serial path for <=1 shard; embedder.py) | embedder fetches shard JSONs serially | per-shard get_object loop (embedder.py:79-84); embed not bottleneck | this commit |
| R-10 | P2 | fixed this round — **REGRESSED in 2.0, see R-27** | citations show no pages (chunks.page_start/end NULL) | stitch drops page metadata; chunking never sets it | this round |
| R-11 | P1 | fixed 2026-09-21 (f1f0415) | retry `scope=shards` resets shards to pending but enqueues an embed job the embedder ignores → failed shards never re-parsed; doc flips ready/partial with a stranded pending shard | no ParseJob enqueued; embedder selects `state=="done"` only | f1f0415 |
| R-12 | P0 | fixed 2026-09-21 (f1f0415) | second document's chunks overwrite first document's Qdrant points; citations can attribute the wrong book | point id derived from chunk_hash alone; UNIQUE(doc_id, chunk_hash) is composite; hydrate keys rows by chunk_hash only | f1f0415 |
| R-13 | P3 | fixed 2026-09-21 (dba6f06) — **REGRESSED in 2.0, see R-27** | all windows of a long section cite the section's full page span | `_emit` indexes the page map with the section's line bounds, not the window's | dba6f06 |
| R-14 | P2 | fixed 2026-09-21 (7374de4) | API key scoped to collection A returns < top_k when B-chunks fill slots | key-level collection scope post-filters after Qdrant top_k truncation (MCP path pushes down) | 7374de4 |
| R-15 | P2 | fixed 2026-09-21 (94c4484) | dedupe key and stored hash are client-controlled | commit stores `body.content_sha256` without reading the object (PRD §6.1 says API computes it) | 94c4484 |
| R-16 | P3 | fixed 2026-09-21 (94c4484) | TEI embed path batches at a budget pinned to ollama's 2048 ceiling; tokenizer reloaded per job | `ctx_budget = 1900` + `from_pretrained("BAAI/bge-m3")` hardcoded in handle_embed | 94c4484 |
| R-17 | P2 | fixed 2026-09-21 (7374de4) | one ~12MB blocking upsert per book re-creates the 2026-09-11 timeout-loop shape | `upsert_chunks` sends all points in a single `wait=True` request | 7374de4 |
| R-18 | P3 | fixed 2026-09-21 (39e00cf) | `chunks.embedded_at` never stamped; re-embed tooling cannot identify stale vectors | embedder insert omits the column | 39e00cf |
| R-19 | P3 | fixed 2026-09-21 (c3893cc) | eval JSON reports `hit_at_{top_k}` carrying the hit@8 value; undercounts for top_k > 8 | run.py conflates judge's fixed hit@8 with run top_k | c3893cc |
| R-20 | P3 | fixed 2026-09-21 (c3893cc) | `/pipeline` shows a dead Redis as empty lanes while `/queues` propagates | lanes loop `except Exception: pass` | c3893cc |
| R-21 | P2 | fixed 2026-09-21 (94c4484) | non-PDF / over-cap PDFs are discovered inside a parser instead of rejected at commit | no magic-byte / page-count validation (PRD §11) | 94c4484 |
| R-22 | P1 | fixed this round | every parser shard converts the ENTIRE PDF: a 300-page book with 16–24-page shards runs docling/anydoc over all 300 pages N times (~2.5 min/shard ⇒ 40–50 min of pure duplicated conversion) | `HandleParse` passes the full downloaded `sourcePath` as `ParseRequest.PDFPath` and both tiers (anydoc, docling) convert whatever file they are handed; the page range is metadata only | this commit |
| R-23 | P1 | fixed this round | a `-tags anydoc` build cannot link: `internal/pipeline/anydoc.go` calls `C.anydoc_convert`/`C.anydoc_free_buffer` — symbols that do not exist in `third_party/anydoc-go/include/anydoc.h` (real ABI: `anydoc_to_markdown_bytes` + `anydoc_string_free`); `fileformat.go`'s format table (PDF=0…TXT=4) also disagrees with the real ABI tags (PDF=3, DOCX=1, PPTX=5, XLSX=8), so PDF would have been sent as tag 0 (= DOC) | hand-rolled CGO preamble drifted onto a nonexistent ABI while the complete, correct binding already sat in `third_party/anydoc-go/`; Dockerfile has no rust toolchain and no `WITH_ANYDOC` lane | this commit |

| R-24 | P0 | fixed this round | embedder fails with `duplicate key value violates unique constraint "chunks_pkey"` then `current transaction is aborted … (SQLSTATE 25P02)`; embed never completes on re-delivered jobs or repeated boilerplate | `tx.CopyFrom` (COPY) carries no `ON CONFLICT`, so a unique violation (`chunks_pkey` / `uq_chunk_hash`) aborts the tx and the row-by-row `ON CONFLICT DO NOTHING` fallback then dies with 25P02 on the aborted tx; intra-batch duplicate hashes (`DeterministicChunkID(doc_id, hash)` collision, no prior row needed) and non-adjacent duplicates surviving the adjacency-only chunker dedupe reach the same violation | this commit |
| R-25 | P2 | fixed this round (compose service removed; `janitor` subcommand kept as a manual ops escape hatch) | two janitor passes run concurrently against the same Postgres+Redis — `stuckWarn` writes duplicate `events` rows at 2× rate and both instances upsert the same `metrics_rollup` minute bucket (last-writer-wins, `queue_depth` can flap) | `serve` runs the janitor as a goroutine (2.0 consolidation, `cmd/lybrix-server/main.go` runServe) while `deploy/docker-compose.yml` still ships the 1.0 `lybrix-janitor` service (`command: ["janitor"]`); both landed in the same rewrite commit (78b6bfe) and no coordination exists — the only advisory lock in the codebase guards schema/dim-index creation, not the janitor pass, and `lastRollupBucket` is a process-local global | this commit |
| R-26 | P1 | fixed this round | chunk text is a flattened word stream: tables, code fences, list nesting and paragraph breaks are destroyed before indexing, so BM25 and dense retrieval both see word soup and the `read_pages` field named `markdown` is not markdown (`"Results\n\n| Model | Score | \| --- \| ---: \| \| anydoc \| 0.91 \| \`\`\`go func main() …"`) | `sectionText` (chunker.go:173-183) and `childWindows` (chunker.go:355-361) both discard line structure with `strings.Fields` + `strings.Join(words, " ")`; the parser's GFM output never survives to the index | this commit |
| R-27 | P1 | fixed this round | every child chunk row carries NULL `page_start`/`page_end` — search citations show no pages and `read_pages` returns only parents (77 of 110 hits in `scripts/eval/results/baseline-20260919T091202Z.json` cite `page_start: null`) | `AttachChildPages` is the only writer of child page ranges and is called ONLY from `chunker_test.go:99` — never from `HandleEmbed`; the Go port of R-10/R-13 regressed the 1.0 fix (the stitch page map is in hand at `service/embedder.go:72` and simply unused) | this commit |
| R-28 | P2 | fixed this round | parent→child linking is approximate and silently lossy, and `seq` collides between parents and children so `get_chunk_context` and REST `prev_id`/`next_id` can return the wrong kind of chunk | `LinkParentsByHash` (store/chunks.go:185-203) joins on `seq` proximity rather than by hash and does `_ = err; return nil`, discarding any failure; `Seq` is assigned per-kind (`len(parents)` at chunker.go:223, `len(children)` at chunker.go:248) so the two sequences overlap | this commit |
| R-29 | P2 | fixed this round | `StitchedDoc.Pages` is longer than the markdown it accompanies, so every page number derived from it is shifted; a shard whose payload ends in `\n` (docling's always does) contributed an extra entry for the empty trailing element, and `strings.TrimSpace` on the joined result removed lines the map still counted | `Stitch` built the page map from each shard's raw `strings.Split` output while trimming the returned markdown (`pipeline/stitch.go`), and nothing consumed `Pages` so the drift was invisible — it surfaced only when the chunker began indexing the map by line offset | this commit |
| R-30 | P2 | fixed this round | a chunk's `Start`/`End` are wrong by the length of a re-injected table header, so page attribution is off by up to a page and any offset-based reconstruction slices the wrong text (202 of 211 children in a 400-row-table fixture violated the `End-Start == runeLen(Content)` invariant) | the child offset shift `sub.Start += parent.Start` is only valid when the parent's `Content` is a pure source slice; a parent carrying a re-injected header has generated runes prepended, so children re-split from it index a buffer that does not begin at `parent.Start`; the same omission existed in the heading and heuristic splitters, which remapped `Start`/`End` but dropped `SyntheticPrefixRunes` | this commit |
| R-31 | P2 | fixed this round | `read_pages` returns an empty result for any document whose sections are all short, because such a doc has no parent rows at all | `SplitParentChild` does not materialise a parent for a section yielding one identical child (storing both would collide on the content hash, and the surviving parent row is never embedded — the doc would become unsearchable), so a parents-only query matched nothing; the fix prefers parents but falls back to children when no parent overlaps the range | this commit |

## 2.0 rewrite disposition (2026-09-21)

The 1.0 Python stack was replaced in place by the Go rewrite (hard cut —
corpus re-ingested, no dual run). Every R-1..R-21 fix above was 1.0-code
specific; the Go port re-implements the *behavior* each fix established,
and the fix must be re-validated against the Go code rather than assumed:

- **Re-implemented in the Go port (behavior carried):** R-1 embed-sweep
  dedup (`PendingJobDocIDs`, `internal/worker/janitor.go`); R-2 embed
  terminal cap (`embedMaxAttempts`, `internal/service/embedder.go`); R-3
  lag fallback (`scanUndeliveredTail`, `internal/queue/streams.go`); R-4
  XDEL-on-ack (`queue.Ack`); R-5/R-20 full-count aggregates + lane error
  propagation (`internal/api/system.go`); R-6 split requeue dedup +
  no-blind-re-add (`janitor.requeueSweep`); R-7 recycle-after on a job
  boundary (`RunConsumer` recycleAfter); R-8 delivery-cap quarantine,
  fail-open (`janitor.reclaim`); R-9 parallel S3 prefetch
  (`pipeline.Prefetch`); R-10 per-line page map (`pipeline.Stitch`; the
  map is consumed by `chunker.newPageMapper`, which replaced the deleted
  `AttachChildPages`/`findWordSpan` offset-recovery hack — see R-27);
  R-11 retry scope=shards requeues
  ParseJobs + unwinds shards_failed
  (`store.RequeueFailedShards` + api retry handler); R-12 deterministic
  chunk ids keyed by (doc_id, chunk_hash) (`store.DeterministicChunkID`);
  R-13 per-window page ranges (each chunk's range is derived from its own
  rune offsets against the stitch page map); R-14
  key scope pushed into both RRF CTEs (`store.HybridSearch`); R-15/R-21
  server-side sha256 + `%PDF-` magic + page-cap verify at commit
  (`api.verifyRawObject`); R-16 token budget from settings
  (`PlanBatches`); R-17 batched vector writes (`UpdateEmbeddingTx` per
  batch); R-18 `embedded_at` stamped on vector write; R-19 `hit_at_top_k`
  (`cmd/lybrix-eval/run.go`).
- **Superseded by design:** Qdrant-related rows (R-2's Qdrant timeout
  surface, R-12's Qdrant overwrite shape, R-17's Qdrant upsert) — the
  vector store is ParadeDB now; the *loop shapes* those fixes encoded
  (terminal caps, composite-key identity, batched writes) are the carried
  part.
- **Still open:** none carried as open rows; new incidents land as fresh
  rows per the ledger contract.
