# rag-platform — Improvement + Bug-Hardening Plan (round 1)

## Context

Corpus backfill is the burning priority: ~9.1k shards pending, ETA ~30-35h at 3.7-4.4 shards/min across 6 CPU-saturated parsers. The five 2026-09-12 queue-hygiene incidents are fixed and already partly test-covered. This plan ranks the 5 focus areas against what the code actually shows and scopes ONE implementer session.

### Key findings from code survey (drive the ranking)

- **Converter rebuild confirmed** — `parser.py:102` calls `build_converter()` (libs/parsing/src/parsing/converter.py:24) inside `handle_parse`, i.e. once per shard job. `DocumentConverter(...)` construction is per call; docling initializes the pipeline (layout-model load + HF artifact resolution/HEAD requests) on first `convert()` of each new converter instance. On hosts parsing a shard every ~13-16s, re-paying pipeline init each shard is plausibly 20-40% wasted CPU. Highest value, lowest risk.
- **Stage overlap is LOW value — defer.** The whole-book barrier delays a book's embed, but embed jobs from *previously settled books* keep the embedder fed continuously during the backfill (embed is confirmed NOT the bottleneck: backlog empty, 713 chunks/min). Overlap would only shave a ~3-4 min tail per book. Also: `PARSER_RECYCLE_AFTER=10` is set in compose but **never implemented** in parser.py — so parsers are effectively long-lived processes, which makes the converter cache safe (cache survives many jobs) and removes the "cache dies every 10 jobs" objection.
- **Citation bug confirmed** — parser uploads only `export_to_markdown()` text (parser.py:113-116); `stitch()` (libs/parsing/src/parsing/stitch.py:37) concatenates markdown lines and returns `{"texts": [], "markdown": ...}` with no page info; `chunk_markdown` (libs/chunking/src/chunking/hybrid.py:38) never sets `page_start/page_end` (they default `None` in the `Chunk` dataclass, hybrid.py:34-35); embedder inserts `c.page_start/c.page_end` = NULL (embedder.py:114-115, 143-144). Columns exist (models.py:163-164, migration 0001) and API/MCP already serve them (search.py:67-68, server.py:66-67) — so the fix is purely in the stitch→chunk path. PRD G4 explicitly promises "search and cite with page numbers".
- **Queue hygiene tests mostly exist** — tests/test_ack_trim.py (trim-on-ACK, reclaim trims old entry), test_janitor_embed_sweep.py (flood dedup + scan-failure no-blind-add), test_embedder_retry_cap.py, test_undelivered_count.py, test_queue_depth.py. Missing: janitor escalation invariant (unbounded-retry guard for parse) and a net-stream-growth invariant for the reclaim path. Testing convention: MagicMock/FakeRedis, no fakeredis, no real services.
- **New bugs found (→ BACKLOG.md, not fixed this round):**
  1. **doc.split requeue flood risk** — janitor.py:141-147 re-XADDs a split job for every UPLOADED doc *every pass, unconditionally* — same bug class as the 2026-09-11 embed flood (no `pending_embed_docs`-style dedup for doc.split). Low probability (split jobs ACK fast) but the guard is missing.
  2. **PARSER_RECYCLE_AFTER never implemented** — compose sets it (deploy/docker-compose.yml:220), parser.py docstring claims it, no code reads it. Long-lived parser processes accumulate memory; SoftOOM (6144→4608MB) is the only backstop.
  3. **Runner has no DLQ** — runner.py docstring promises "DLQ after max attempts"; nothing implements it. A doc deleted mid-parse loops via reclaim → raise → re-deliver forever (delivery count unbounded on doc.parse).
  4. **Serial S3 fetch in embedder** — embedder.py:79-84 fetches N shard JSONs one-by-one (minor; embed is not the bottleneck).

## Ranked focus areas

| # | Area | Value | Risk | This round? |
|---|------|-------|------|-------------|
| 1 | Converter cache (perf) | High — attacks the actual bottleneck, ~20-40%/parser est. | Low (pure cache, build_converter untouched) | **YES** |
| 5 | docs/BACKLOG.md | High (incident memory), trivial cost | None | **YES** |
| 4 | Queue-hygiene regression tests | High (locks 5 incidents) | None (tests only) | **YES** |
| 3 | Citation page metadata | High (product promise G4 broken today) | Medium (3 files, but additive) | **YES** |
| 2 | Stage overlap (extract prefetch) | Low now (embed already overlapped cross-book; embed not bottleneck) | Medium (new staging state) | **DEFER** |

## Exact edits

### A. Module-level converter cache (focus 1)

**`libs/parsing/src/parsing/converter.py`** — add a cached accessor; keep `build_converter()` byte-identical (tests/test_converter_ocr.py constructs through it directly):

```python
_CONVERTER_CACHE: dict[tuple, DocumentConverter] = {}

def converter_cache_key(need_ocr: bool, s: Settings) -> tuple:
    return (need_ocr, s.parsing_ocr_engine.lower(), s.parsing_ocr_lang,
            s.parsing_ocr_text_score, s.parsing_torch_threads,
            s.parsing_do_table_structure, s.parsing_accelerator_device)

def get_converter(need_ocr: bool, settings: Settings | None = None) -> DocumentConverter:
    """Module-level cache: DocumentConverter init is per-process expensive
    (pipeline init + HF artifact resolution happen on first convert()). Parsers
    are long-lived (PARSER_RECYCLE_AFTER is configured, not implemented), so the
    cache pays off across the process lifetime. Max 2 entries (OCR on/off)."""
    s = settings or get_settings()
    key = converter_cache_key(need_ocr, s)
    conv = _CONVERTER_CACHE.get(key)
    if conv is None:
        conv = build_converter(need_ocr=need_ocr, settings=s)
        _CONVERTER_CACHE[key] = conv
    return conv
```

**`services/workers/src/workers/parser.py:102`** — one-line change: `converter = get_converter(need_ocr=verdict.needs_ocr, settings=s)` (+ import). Add a `logger.info("converter cache %s need_ocr=%s", "HIT"|"MISS", ...)` line so the implementer can measure init savings from container logs.

**New test `services/workers/tests/test_converter_cache.py`** (mock `build_converter` to count constructions):
- `test_get_converter_same_key_returns_same_instance` — two calls, same settings+need_ocr → identical object, `build_converter` called once
- `test_get_converter_different_need_ocr_builds_separately` — OCR on vs off → two instances
- `test_get_converter_settings_change_rebuilds` — changed `parsing_ocr_engine` → new instance
- `test_cache_hit_does_not_rebuild` — third call adds no construction

Expected win (from code, no benchmark): pipeline init + HF HEAD/model-load paid once per (need_ocr, settings) per process instead of once per shard. At ~13-16s/shard and est. 3-8s init per shard, projected ~20-40% parser throughput lift ≈ 6-12h off the 30-35h ETA across 6 parsers.

### B. Page metadata through stitch → chunking (focus 3)

Design: line-number→page mapping built at stitch time (stitch knows shard boundaries; chunking already walks lines).

**`libs/parsing/src/parsing/stitch.py`**:
- `load_shard_docs(fetch)` — unchanged.
- `stitch(docs, shard_page_ranges=None)` — new optional arg: list of `(page_start, page_end)` aligned with `docs` (doc order = sorted shard idx). While joining lines, build `page_of_line: list[int]` (0-based line index → 1-based page; a line from shard i maps to `page_start_i + floor(local_line * pages_in_shard / lines_in_shard)` — proportional within shard, monotone, good enough for citations). The boundary-heading drop adjusts the map in lockstep with `doc_lines` trimming. Return shape: `{"texts": [], "markdown": ...}` unchanged when `shard_page_ranges is None`; add `"pages": page_of_line` when given.

**`libs/chunking/src/chunking/hybrid.py`**:
- `_sections(markdown)` yields `(heading_path, body, start_line)` (line index into the markdown) — purely internal.
- `chunk_markdown(markdown, tokenizer=None, max_tokens=512, min_tokens=0, pages=None)` — new optional `pages: list[int] | None`. `_emit` gains `page_start/page_end` computed as `pages[start_line]`/`pages[end_line]` when `pages` is provided; `None` → `page_start/page_end=None` (back-compat: every existing caller unchanged, chunk hashes unchanged since text is unchanged).

**`services/workers/src/workers/embedder.py`** (~line 88-92): `shard_page_ranges = [(sh.page_start, sh.page_end) for sh in shard_rows]` (shard_rows are `state=="done"`, already `order_by(Shard.idx)` — aligned with `load_shard_docs`'s sorted keys; failed shards have no JSON so no misalignment), pass `stitch(load_shard_docs(fetch), shard_page_ranges)` and `chunk_markdown(..., pages=stitched.get("pages"))`.

**Tests**:
- `tests/test_stitch.py` (extend): `test_stitch_with_page_ranges_builds_line_page_map` (monotone, shard 2 lines map into shard 2's page range); `test_stitch_boundary_heading_drop_adjusts_page_map` (dropped duplicate heading shifts mapping by one); `test_stitch_without_ranges_has_no_pages` (back-compat).
- `tests/test_chunking.py` (extend): `test_chunks_carry_page_range_when_pages_given`; `test_chunks_pages_none_when_pages_absent`.
- `services/workers/tests/test_embedder_pages.py` (new, mock-style of test_parser_cache.py): `handle_embed` passes shard page ranges to stitch and page fields land in the PG insert — assert via patched `pg_insert`-executed stmts or simpler: patch `parsing.stitch.stitch` and assert called with ranges.

Note in plan: existing 231 ready docs keep NULL pages (chunk_hash ON CONFLICT DO NOTHING) — only newly embedded books get pages; acceptable.

### C. Queue-hygiene regression tests (focus 4)

**New `services/workers/tests/test_janitor_hygiene.py`** (MagicMock style of test_janitor_embed_sweep.py):
- `test_escalated_shard_marked_failed_with_event` — pending shard, `attempts >= settings.max_shard_attempts` → state failed, `shards_failed` bumped, error event written (locks the "unbounded retry" incident invariant for parse).
- `test_escalation_only_touches_max_attempt_shards` — attempts < max untouched.
- `test_reclaim_never_grows_stream` — janitor_pass with K reclaimed entries per stream: every `xadd` is matched by an `xdel` of the old entry; net XLEN delta ≤ 0 (locks the 2026-09-12 "36% dupe stream" invariant: reclaim re-add + ACK + XDEL, never re-add alone).
- `test_requeue_uploaded_doc_gets_single_split_job` — documents the CURRENT requeue behavior (one XADD per pass); plus a marked-expected-failure style comment documenting the known doc.split flood gap (BACKLOG R-6) so the fix lands with a test ready.

(Stream-trim-on-ACK invariant is already locked by tests/test_ack_trim.py — no duplicate work.)

### D. docs/BACKLOG.md (focus 5)

Lightweight table, matching repo's terse docstring style. Seed rows:

| id | priority | status | symptom | root cause | fix commit |
|----|----------|--------|---------|-----------|------------|
| R-1 | P0 | fixed 2026-09-12 (fbcbf59) | janitor flooded doc.embed (~3.2k dup jobs) | settled-book sweep had no dedup vs undelivered jobs | fbcbf59 |
| R-2 | P0 | fixed 2026-09-12 (fbcbf59) | embed job retried forever on Qdrant timeout, doc stuck parsing | no terminal retry cap on embed failures | fbcbf59 |
| R-3 | P1 | fixed 2026-09-12 (07ad045, ea1614c) | dashboard waiting=1 for 73-job backlog (×2 endpoints) | XINFO lag=None after XDEL treated as 0 | 07ad045/ea1614c |
| R-4 | P1 | fixed 2026-09-12 (549e10e) | doc.parse 25,073 entries / 15,994 shards (36% dupes) | reclaim re-added ACKed PEL entries; nothing trims streams | 549e10e |
| R-5 | P1 | fixed 2026-09-12 (07ad045) | dashboard 'in-flight' card wrong granularity | doc-count read from ?limit=200 list | 07ad045 |
| R-6 | P1 | open | doc.split flood risk: janitor re-XADDs split job for every UPLOADED doc every pass | no undelivered-dedup on requeue step (janitor.py:141-147); same class as R-1 | — |
| R-7 | P2 | open | parser memory grows unbounded over days | PARSER_RECYCLE_AFTER configured in compose, never implemented in parser.py | — |
| R-8 | P2 | open | vanished-doc parse jobs redeliver forever | runner docstring promises DLQ; none implemented; reclaim re-adds poison entries | — |
| R-9 | P3 | open | embedder fetches shard JSONs serially | per-shard get_object loop (embedder.py:79-84); embed not bottleneck | — |
| R-10 | P2 | planned this round | citations show no pages (chunks.page_start/end NULL) | stitch drops page metadata; chunking never sets it | this round |

## Acceptance criteria

Implementer runs, all must pass clean:

```bash
uv run pytest tests/ services/workers/tests/ -q        # full suite, incl. new tests
uv run ruff check .                                     # clean
```

- New/extended tests named above all exist and pass: test_converter_cache.py (4), test_janitor_hygiene.py (4), test_stitch.py +3, test_chunking.py +2, test_embedder_pages.py (1+)
- `docs/BACKLOG.md` exists with all 10 rows
- Existing tests untouched-in-behavior: test_converter_ocr.py, test_parser_cache.py, test_stitch.py legacy cases, test_ack_trim.py pass without modification (back-compat proof)
- No docker/compose changes, no commits

## Risks

- **Converter cache memory**: up to 2 loaded converter variants (OCR on/off) per parser process; layout models load lazily on first convert. Soft RSS budget 4608MB has headroom (measured peak 3.0-3.1GB); SoftOOM still guards. Mitigation if needed later: LRU maxsize=1 or drop OCR variant after use.
- **Page mapping fidelity**: proportional line→page interpolation, not docling `prov` provenance (parser exports markdown text only, provenance is not available without re-parsing). Page ranges are approximate (±1 page within a shard); acceptable for citation navigation; documented in BACKLOG row R-10.
- **Stitch signature change**: `stitch(docs, shard_page_ranges=None)` is additive; only embedder passes the new arg. `load_shard_docs` untouched.
- **chunk_markdown signature change**: keyword-only `pages=None` default keeps all existing callers and chunk hashes identical (text unchanged → no re-churn of the 231 ready docs).
- **Scope**: B touches 3 source files + 3 test files; if the session runs tight, land A + D first (independent), then C (tests only), then B last.
