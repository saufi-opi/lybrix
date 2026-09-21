# lybrix — Review Validation (2026-09-20 Hermes review) + Fix Plan

## Context

A full-code review (pasted in the planning brief) was produced from a clone at 2026-09-20 ~21:20 UTC+8. HEAD has since moved only by web/eval commits (`431d1ad`, `f606e39`, `7b38e6d`, `d6cc76d`, `937b9a1`) — none touch the libs/api/workers code the review cites, so every finding was validated against effectively the same backend code. This document (1) classifies every finding REAL / HALLUCINATION / ALREADY-FIXED / PARTIAL with file:line evidence read at HEAD (`937b9a1`), and (2) plans fixes for the validated findings in the review's §6 order.

---

# PART 1 — VALIDATION VERDICTS

## §2 Priority defects

### 2.1 Retry scope `shards` never re-parses the retried shards — **REAL**
- `services/api/src/api/routers/documents.py:161-167`: `scope=="shards"` resets failed shards to `pending`/clears error cols, then XADDs **`contracts.EmbedJob(doc_id=doc_id)`** — no `ParseJob` is ever sent.
- `services/workers/src/workers/embedder.py:100-108`: embedder selects `Shard.state == "done"` only — a `pending` shard is invisible to it.
- `services/workers/src/workers/janitor.py:240-247`: settled-book sweep requires `doc.state == DocState.PARSING`; a doc left in `partial`/`ready` with a stranded pending shard is never revisited. Reverified: nothing else re-enqueues parse work for that shard. The "Retry failed shards" button (PRD §8.3) is a silent no-op re parse.

### 2.2 Qdrant point-id collision across documents — **REAL**
- `libs/retrieval/src/retrieval/qdrant.py:107-110`: `point_id_for` = `uuid.uuid5(uuid.NAMESPACE_URL, f"chunk:{chunk_hash}")` — **chunk_hash alone** (the literal `chunk:` prefix differs trivially from the review's quote; immaterial).
- `libs/core/src/core/db/models.py:151`: `UniqueConstraint("doc_id", "chunk_hash")` — uniqueness is composite only. Identical normalized text in two docs → same chunk_hash → same Qdrant point id → `upsert_chunks` (qdrant.py:126,139) silently overwrites the first doc's point.
- `libs/retrieval/src/retrieval/search.py:122-127`: `hydrate()` keys rows by `chunk_hash` from `Chunk.chunk_hash.in_(hashes)` with **no doc_id filter** — wrong-document attribution is real. Same defect propagates to `scripts/ops_backfill_sparse.py:139` (also calls `point_id_for(chunk_hash)`).

### 2.3 Window-level page citations are coarse — **REAL**
- `libs/chunking/src/chunking/hybrid.py:68,74-83,104-105`: `_emit` is called with the **section's** `start_line`/`end_line` and every window in the section gets `page_start = pages[start_line]`, `page_end = pages[end_line]`. `libs/parsing/src/parsing/stitch.py:83-88` builds the per-line page map. A long section ⇒ every window cites the whole section's page span. PRD §7.2 (prd.md:462-466, citation triple) degraded to section granularity.

### 2.4 API search post-filters scope after top_k truncation — **REAL**
- `services/api/src/api/routers/search.py:52-54`: `body.collection` **is** pushed down via `hybrid_search(collection_id=...)`; but lines 58-60 post-filter `hits` by the **key's** `collections` **after** `rs.hybrid_search` already truncated to `top_k` (Qdrant `limit=top_k`, retrieval/search.py:109). A key scoped to collection A gets < top_k (or zero) when B-chunks occupy slots.
- `services/mcp/src/mcp_server/server.py:61-71,272`: MCP passes `collection` into `build_filter` (pushed down) and *raises* on out-of-scope keys (`assert_collection_allowed`) — surfaces have drifted. Core claim verified.

### 2.5 Commit trusts a client-supplied content hash — **REAL**
- `services/api/src/api/routers/documents.py:71,80`: `body.content_sha256` (schemas.py:24, plain 64-char field) is used for dedupe (`repo.find_duplicate`) and stored on the document row. `libs/core/src/core/storage/s3.py` never reads the object back. PRD §6.1 step 4 (prd.md:257): "API computes sha256 (streaming, from MinIO)". Spoofable dedupe key; raw-copy provenance not verified server-side. No contradicting code exists.

### 2.6 Embedder hardcodes model-specific token budget — **REAL**
- `services/workers/src/workers/embedder.py:174-176`: `AutoTokenizer.from_pretrained("BAAI/bge-m3")` inside `handle_embed` (reloaded from disk every job); line 185: `ctx_budget = 1900` hardcoded. `libs/core/src/core/config.py:5` states nothing hardcodes a model name; `s.embed_model`/`s.embed_backend` exist (config.py:44-45, default backend `tei`). All as described.

### 2.7 Whole-book Qdrant upsert in a single `wait=True` call — **REAL**
- `libs/retrieval/src/retrieval/qdrant.py:139`: `client.upsert(collection_name=name, points=qpoints, wait=True)` with **all** points of the book in one blocking call, invoked from `services/workers/src/workers/embedder.py:219-230`. The 2026-09-11 Qdrant-upsert-timeout incident is real (embedder.py docstring lines 8-13; docs/BACKLOG.md R-2).

## §3 PRD-promised capabilities

| Row | Verdict | Evidence |
|---|---|---|
| Retry-ladder sub-sharding | **REAL (stubbed)** | `services/workers/src/workers/parser.py:12-14` — steps 2-4 marked `TODO M2`; prd.md:343-349 promises the ladder |
| `metrics_rollup` writer | **REAL (absent)** | Table created in `migrations/versions/0001_initial.py:128-143`; **no** `MetricsRollup` model in `libs/core/src/core/db/models.py`, no writer anywhere (repo-wide grep: only migration + PRD hits); janitor (`janitor.py`) never touches it |
| SSE `/v1/events/stream` | **HALLUCINATION** | `services/api/src/api/routers/events.py:34-79` implements `GET /v1/events/stream` returning `StreamingResponse(..., media_type="text/event-stream")`; registered in `services/api/src/api/main.py:25`; `services/api/pyproject.toml:7` even advertises "SSE" |
| Integration test lane (testcontainers) | **REAL (absent)** | No `tests/integration/` dir; zero testcontainers references repo-wide; `.github/workflows/ci.yml:38` runs only `uv run pytest tests/ -q` (root DB-free suite; `services/workers/tests/` is not even in CI) |

## §4 Minor findings

1. **README drift (`make scale`/`make drain`) — REAL.** README.md:56-57 promises both; `Makefile` (full read) has neither; `deploy/docker-compose.yml:228,254,276,298` uses discrete `worker-parser`/`-2`/`-3`/`-4` services.
2. **Unauthenticated read endpoints — REAL.** `documents.py:91-99` (list), `:110-113` (get), `:120-123` (shards) and `collections.py:22` (list_collections) take only `get_session` — no `require_scope`. (Mitigated by internal binding, as the review notes.)
3. **`chunks.embedded_at` never set — REAL.** Column exists (`models.py:166-168`, migration 0001:97); repo-wide grep shows **no writer**.
4. **Upload validation missing — REAL.** `commit` (documents.py:51-88) performs backlog/dedupe only; no magic-byte or page-count check. PRD §11 (prd.md:630) requires "magic-byte validation, size cap, filename sanitisation, and page-count cap".
5. **`search_query:` prefix — REAL (observation).** Both surfaces prefix: `search.py:40` and `server.py:54`. E5 convention applied to bge-m3; A/B-able via `scripts/eval/run.py`.
6. **Eval JSON key bug — REAL.** `scripts/eval/run.py:214`: key `f"hit_at_{args.top_k}"` carries `summary.overall.hit_at_8`. For `top_k > 8` the judge counts only ranks ≤ 8 (`scripts/eval/judge.py:149-150`), so the labelled metric undercounts; for `top_k < 8` the label misnames a correct value. Console print (run.py:239) repeats the conflation.
7. **`/pipeline` swallows exceptions — REAL.** `services/api/src/api/routers/system.py:161` `except Exception: pass` in the lanes loop — a dead Redis reads as `waiting: None` lanes, while `/queues` (system.py:84-100) documents and implements "errors propagate". Also line 128 swallows for `embed_backend` (acceptable, has explicit `"down"` fallback). Core claim verified for the lanes loop.

**No finding was ALREADY-FIXED** — HEAD's post-clone commits touch only web UI, eval results, and docs.

**Summary: 7/7 defects REAL (one with a trivial quote nit), 3/4 §3 rows REAL / 1 HALLUCINATION (SSE), 7/7 minors REAL.**

---

# PART 2 — PLAN (validated findings only, review §6 order)

**Excluded:** SSE stream (HALLUCINATION — endpoint exists; no work planned). Integration-test lane is REAL but is **not** in the review's §6 fix order — out of scope per brief. Minor #5 (prefix A/B) is planned as an experiment toggle only.

## Phase 0 — BACKLOG rows (CLAUDE.md: ledger before code)

Add to `docs/BACKLOG.md` (continue R-numbering), one row each, `status: open` → flipped to `fixed <commit>` as each lands:

- **R-11** (P1) retry `scope=shards` resets shards to pending but enqueues an embed job the embedder ignores → failed shards never re-parsed; doc flips ready/partial with a stranded pending shard | no ParseJob enqueued; embedder selects `state=="done"` only | this fix
- **R-12** (P0) second document's chunks overwrite first document's Qdrant points; citations can attribute the wrong book | point id derived from chunk_hash alone; UNIQUE(doc_id, chunk_hash) is composite; hydrate keys rows by chunk_hash only | this fix
- **R-13** (P3) all windows of a long section cite the section's full page span | `_emit` indexes the page map with the section's line bounds, not the window's | this fix
- **R-14** (P2) API key scoped to collection A returns < top_k when B-chunks fill slots | key-level collection scope post-filters after Qdrant top_k truncation (MCP path pushes down) | this fix
- **R-15** (P2) dedupe key and stored hash are client-controlled | commit stores `body.content_sha256` without reading the object (PRD §6.1 says API computes it) | this fix
- **R-16** (P3) TEI embed path batches at a budget pinned to ollama's 2048 ceiling; tokenizer reloaded per job | `ctx_budget = 1900` + `from_pretrained("BAAI/bge-m3")` hardcoded in handle_embed | this fix
- **R-17** (P2) one ~12MB blocking upsert per book re-creates the 2026-09-11 timeout-loop shape | `upsert_chunks` sends all points in a single `wait=True` request | this fix
- **R-18** (P3) `chunks.embedded_at` never stamped; re-embed tooling cannot identify stale vectors | embedder insert omits the column | this fix
- **R-19** (P3) eval JSON reports `hit_at_{top_k}` carrying the hit@8 value; undercounts for top_k > 8 | run.py conflates judge's fixed hit@8 with run top_k | this fix
- **R-20** (P3) `/pipeline` shows a dead Redis as empty lanes while `/queues` propagates | lanes loop `except Exception: pass` | this fix
- **R-21** (P2) non-PDF / over-cap PDFs are discovered inside a parser instead of rejected at commit | no magic-byte / page-count validation (PRD §11) | this fix

*(README drift, read-endpoint auth, and the prefix A/B are docs/hardening/experiment — not defects; no ledger rows.)*

---

## Phase 1 — R-11 (retry shards) + R-12 (point-id collision) — silent data-integrity

### 1a. R-11: `scope=shards` re-parses the retried shards

**Objective:** After resetting failed shards to `pending`, set the doc back to `PARSING`, enqueue one `ParseJob` per requeued shard, and keep the `shards_failed` counter consistent so the existing last-shard-settled logic re-enqueues embed.

**Edit `services/api/src/api/routers/documents.py`** — replace the `else` branch (lines 161-167):

```python
    else:  # shards: requeue failed shards only
        failed_shards = (
            session.execute(
                select(Shard).where(Shard.doc_id == doc_id, Shard.state == "failed").order_by(Shard.idx)
            )
            .scalars()
            .all()
        )
        if not failed_shards:
            raise HTTPException(status_code=409, detail="no failed shards to retry")
        session.execute(
            Shard.__table__.update()
            .where(Shard.doc_id == doc_id, Shard.state == "failed")
            .values(state="pending", error_code=None, error_detail=None)
        )
        # Counter hygiene: mark_shard_failed bumped shards_failed per failure;
        # requeueing undoes those failures. Without this the embedder would
        # compute a wrong completeness and book_settled could double-count.
        session.execute(
            Document.__table__.update()
            .where(Document.id == doc_id)
            .values(shards_failed=Document.shards_failed - len(failed_shards))
        )
        # Back to PARSING so the janitor's settled-book sweep and stuck-doc
        # warnings see this book again (a stranded pending shard in a
        # terminal-state doc is invisible to every recovery path — R-11).
        repo.set_doc_state(session, doc_id, DocState.PARSING)
        for shard in failed_shards:
            streams.xadd_job(
                r,
                streams.STREAM_PARSE,
                contracts.ParseJob(
                    doc_id=doc_id,
                    idx=shard.idx,
                    page_start=shard.page_start,
                    page_end=shard.page_end,
                    source_uri=doc.source_uri,
                ),
            )
```

(`ParseJob` requires `source_uri` per `libs/core/src/core/queue/contracts.py:32-39`; shard rows carry `page_start`/`page_end` per `models.py:125-126`. `select`, `Document` already imported. When the re-parsed shards settle, `parser.py:142-149`'s `book_settled` check re-enqueues embed — recommendation (c) satisfied with zero parser changes.)

**New test `tests/test_retry_router.py`** (root suite; direct-call pattern of `tests/test_keys_router.py` — no TestClient, no DB): a fake session returning two canned `Shard` rows for the `select(Shard)` and recording update statements; monkeypatch `streams.xadd_job` to record calls. Assert: (1) one `ParseJob` XADD per failed shard with the shard's `idx/page_start/page_end` and `source_uri`; (2) doc state update to `parsing` recorded; (3) `shards_failed` decremented by 2; (4) zero failed shards → `HTTPException 409`; (5) no `EmbedJob` XADD.

**Suite:** root (`tests/`) — api is importable there (`test_keys_router.py` already imports `api.routers.keys`).

### 1b. R-12: doc-scoped Qdrant point ids + doc-scoped hydration

**Objective:** Point id = `uuid5(..., f"chunk:{doc_id}:{chunk_hash}")`; hydration keyed by `(doc_id, chunk_hash)`.

**Edit `libs/retrieval/src/retrieval/qdrant.py:107-110`:**

```python
def point_id_for(doc_id: str, chunk_hash: str) -> str:
    """Qdrant accepts UUIDs or unsigned ints as point ids; derive a stable
    UUID5 from doc_id AND chunk_hash so re-embedding upserts, never
    duplicates — and two documents sharing identical normalized text
    (boilerplate, standard clauses) never collide on one point (R-12:
    chunk_hash is unique only per (doc_id, chunk_hash))."""
    return str(uuid.uuid5(uuid.NAMESPACE_URL, f"chunk:{doc_id}:{chunk_hash}"))
```

**Edit `qdrant.py:126`** (in `upsert_chunks`): `id=point_id_for(str(p["doc_id"]), p["chunk_hash"]),`

**Edit `libs/retrieval/src/retrieval/search.py:122-132`** (`hydrate`):

```python
    hashes = list({p.payload.get("chunk_hash") for p in fused_points if p.payload})
    doc_ids = list({p.payload.get("doc_id") for p in fused_points if p.payload and p.payload.get("doc_id")})
    rows: dict[tuple[str, str], Chunk] = {}
    if hashes:
        stmt = select(Chunk).where(
            Chunk.chunk_hash.in_(hashes),
            Chunk.doc_id.in_([uuid.UUID(d) for d in doc_ids]),
        )
        for c in session.execute(stmt).scalars():
            rows[(str(c.doc_id), c.chunk_hash)] = c
    ...
        chunk = rows.get((str(payload.get("doc_id")), payload.get("chunk_hash")))
```

**Edit `scripts/ops_backfill_sparse.py:105,139`:** select `(Chunk.doc_id, Chunk.chunk_hash, Chunk.text)`; call `point_id_for(str(doc_id), chunk_hash)`; batch-resume cursor becomes `(doc_id, chunk_hash)`-aware (order by `Chunk.doc_id, Chunk.chunk_hash`; `resume_after` compares the tuple). Tests `tests/test_ops_backfill_sparse.py:94-95,118,132` update to the 2-arg call with row doc_ids.

**Tests:** extend `tests/test_retrieval_bm25.py` — (1) `point_id_for("d1", h) != point_id_for("d2", h)` for the same hash, stable per pair (replaces `test_point_id_for_stable` at :242-244); (2) new `hydrate` test with a fake session holding two Chunks sharing one `chunk_hash` under different doc_ids → each fused point resolves to its own doc's chunk (page numbers/title from the right row). Update `tests/test_ops_backfill_sparse.py` assertions.

**Suite:** root (`tests/`).

**Operational note (implementer):** existing points keep old ids → after deploy, run `uv run python -m scripts.reindex` once (Qdrant is a cache per PRD §13.5; Postgres rows are untouched). Old orphaned points can be removed by deleting all points before reindex or by `delete_doc_points` per doc; state this in the deploy note.

---

## Phase 2 — R-14 (search scope push-down) + R-17 (paged upsert) — correctness under load

### 2a. R-14: push key-level collection scope into the Qdrant query

**Objective:** Key scope becomes a `MatchAny` filter inside the Qdrant query; API stops post-filtering truncated hits.

**Edit `libs/retrieval/src/retrieval/search.py:46-57`** (`build_filter`):

```python
def build_filter(
    collection_id: str | None = None,
    doc_id: str | None = None,
    collection_ids: list[str] | None = None,
) -> qm.Filter | None:
    must = []
    if collection_id:
        must.append(
            qm.FieldCondition(key="collection_id", match=qm.MatchValue(value=collection_id))
        )
    if collection_ids:
        # Key-level scope (api keys carry a collections array): MatchAny
        # pushes the scope INTO the Qdrant query so top_k slots are filled
        # from allowed collections only — post-filtering after truncation
        # silently returned fewer than top_k (R-14).
        must.append(qm.FieldCondition(key="collection_id", match=qm.MatchAny(any=collection_ids)))
    if doc_id:
        must.append(qm.FieldCondition(key="doc_id", match=qm.MatchValue(value=doc_id)))
    return qm.Filter(must=must) if must else None
```

**Edit `hybrid_search` (search.py:158-173):** add `collection_ids: list[str] | None = None` param; pass through to `build_filter(collection_id=collection_id, doc_id=doc_id, collection_ids=collection_ids)`.

**Edit `services/api/src/api/routers/search.py:44-60`:**

```python
    qdrant = QdrantClient(url=s.qdrant_url, api_key=s.qdrant_api_key, timeout=5)
    key_scope = list(getattr(key, "collections", None) or [])
    try:
        hits = rs.hybrid_search(
            qdrant,
            COLLECTION_NAME,
            session,
            dense_query=dense,
            sparse_query=_bm25_stub(body.query),
            top_k=min(body.top_k, s.search_max_top_k),
            collection_id=body.collection,
            collection_ids=key_scope or None,
        )
    finally:
        qdrant.close()
    # key-level scope is pushed into the Qdrant filter (R-14) — no
    # post-filter here: filtering after top_k truncation returned fewer
    # than top_k results for scoped keys.
```

(Delete the lines 58-60 post-filter block and `_doc_collection`; body.collection outside key scope still yields zero rows — both conditions are `must`s, preserving prior semantics without the truncation bug.)

**Tests** (`tests/test_retrieval_bm25.py`, root suite): (1) `build_filter(collection_ids=["a","b"])` serializes to a `MatchAny` condition on `collection_id`; (2) combined with `collection_id`, both conditions present; (3) `hybrid_search(..., collection_ids=[...])` forwarding — assert the captured `query_points` kwargs carry the `MatchAny` filter in both prefetches.

### 2b. R-17: page the Qdrant upsert

**Edit `libs/retrieval/src/retrieval/qdrant.py:113-140`** (`upsert_chunks` body, after building `qpoints`):

```python
    # Page the upsert (R-17): one ~12MB wait=True request for a whole book
    # is the most timeout-prone shape available (the 2026-09-11 incident was
    # exactly a Qdrant upsert timeout loop). 500-point pages keep each
    # request small; the runner's retry/cap path then re-sends one page on a
    # transient failure, not the whole book.
    upsert_page = 500
    for i in range(0, len(qpoints), upsert_page):
        client.upsert(collection_name=name, points=qpoints[i : i + upsert_page], wait=True)
    return len(qpoints)
```

**Test** (new `tests/test_qdrant_upsert.py` or extend `test_retrieval_bm25.py`, root suite): fake Qdrant recording `upsert` calls; 1,201 points → 3 calls of sizes [500, 500, 201]; empty list → 0 calls, returns 0; ids in each page are doc-scoped (Phase 1b).

**Suite:** root (`tests/`).

---

## Phase 3 — R-15 (server-side sha256) + R-21 (upload validation) + R-16 (token budget)

### 3a. R-15 + R-21: one streaming pass at commit — hash + magic bytes + page cap

**Objective:** At commit, stream the raw object from MinIO once: verify the client hash, check the `%PDF-` magic, and enforce the PRD §11 page-count cap. (One download serves all three; raw copy in `raw/` becomes a verified original.)

**Edit `services/api/pyproject.toml`:** add `"pypdfium2>=4.30"` to dependencies.

**Edit `libs/core/src/core/config.py`:** add near the ingest settings:

```python
    max_document_pages: int = Field(default=800, description="PRD §11 page-count cap; reject over-cap PDFs at commit")
```

**Edit `services/api/src/api/routers/documents.py`** — add helper and wire into `commit` (before the dedupe check, after the backlog gate):

```python
import hashlib
import tempfile
from pathlib import Path

def _verify_raw_object(s3c, bucket: str, key: str, client_sha: str, max_pages: int) -> None:
    """Stream the raw object once: sha256 + magic bytes + page cap (R-15/R-21).

    PRD §6.1: the API — not the client — owns the dedupe key; §11: reject
    non-PDFs and over-cap books at the door rather than inside a parser.
    Raises HTTPException; the temp file lives only for the pdfium probe."""
    import pypdfium2 as pdfium

    digest = hashlib.sha256()
    with tempfile.NamedTemporaryFile(suffix=".pdf") as tmp:
        obj = s3c.get_object(Bucket=bucket, Key=key)
        stream = obj["Body"]
        head = stream.read(5)
        digest.update(head)
        if head != b"%PDF-":
            raise HTTPException(status_code=400, detail="uploaded object is not a PDF")
        tmp.write(head)
        for chunk in stream.iter_chunks():
            digest.update(chunk)
            tmp.write(chunk)
        tmp.flush()
        if digest.hexdigest() != client_sha:
            raise HTTPException(status_code=400, detail="content_sha256 mismatch")
        try:
            page_count = len(pdfium.PdfDocument(tmp.name))
        except Exception as exc:
            raise HTTPException(status_code=400, detail=f"unreadable PDF: {exc}") from exc
        if page_count > max_pages:
            raise HTTPException(
                status_code=400,
                detail=f"{page_count} pages over cap {max_pages}",
            )
```

In `commit`, after the backlog check:

```python
    raw_key_str = s3.raw_key(doc_id)
    _verify_raw_object(
        s3.make_s3(), get_settings().s3_bucket_raw, raw_key_str, body.content_sha256,
        get_settings().max_document_pages,
    )
```

…and use the **verified** hash (`body.content_sha256`) for dedupe + the row as today (it now equals the server-computed value or the request 400s). Wrap `s3c.get_object` `ClientError` → 400 "object not uploaded" so a skipped PUT reads as a client error, not a 500.

**Tests `tests/test_commit_router.py`** (root suite): fake s3 whose `get_object` returns a body with `read`/`iter_chunks` yielding canned bytes; monkeypatch `pypdfium2.PdfDocument` to return a sized fake. Assert: (1) matching hash + `%PDF-` magic + pages ≤ cap → commit proceeds, SplitJob XADDed; (2) hash mismatch → 400, no XADD, no DB row; (3) wrong magic → 400; (4) pages over `max_document_pages` → 400; (5) MinIO ClientError → 400.

**Suite:** root (`tests/`).

### 3b. R-16: config-plumb the token budget + cache the tokenizer

**Edit `libs/core/src/core/config.py`:** add beside `embed_batch_size` (config.py:47):

```python
    embed_ctx_budget: int = Field(
        default=1900,
        description="Max tokenizer tokens per embed request. Pin per backend: "
        "ollama rejects >2048 of its own tokens (empirically pinned 2026-09-14); "
        "TEI's bge-m3 ctx is 8192, so TEI deployments can raise this ~4x.",
    )
```

**Edit `services/workers/src/workers/embedder.py`:**

Module level (after `PREFETCH_WORKERS`):

```python
_TOKENIZER_CACHE: dict[str, object] = {}


def _get_tokenizer(model: str):
    """Load once per process (R-16): handle_embed used to re-read the
    tokenizer from disk on every job."""
    tok = _TOKENIZER_CACHE.get(model)
    if tok is None:
        from transformers import AutoTokenizer

        tok = AutoTokenizer.from_pretrained(model)
        _TOKENIZER_CACHE[model] = tok
    return tok


def plan_batches(chunks, tok, ctx_budget: int, batch_size: int) -> list[list]:
    """Greedy token-budgeted batching, extracted from handle_embed so the
    budget semantics are unit-testable without transformers or TEI."""
    batches: list[list] = []
    cur: list = []
    cur_tokens = 0
    for c in chunks:
        t = len(tok(c.text, add_special_tokens=False)["input_ids"])
        if t > ctx_budget:
            # pathological single chunk: hard-cut to the token budget
            import dataclasses

            ids = tok(c.text, truncation=True, max_length=ctx_budget,
                      add_special_tokens=False)["input_ids"]
            c = dataclasses.replace(c, text=tok.decode(ids))
        if cur and cur_tokens + t > ctx_budget or len(cur) >= batch_size:
            batches.append(cur)
            cur, cur_tokens = [], 0
        cur.append(c)
        cur_tokens += t
    if cur:
        batches.append(cur)
    return batches
```

In `handle_embed`, replace lines 168-204 with:

```python
        tok = _get_tokenizer(s.embed_model)  # never a hardcoded model name (R-16)
        # Budget comes from Settings — the 1900 default is the ollama pin;
        # TEI's 8192 ctx means the TEI path should raise EMBED_CTX_BUDGET.
        batches = plan_batches(chunks, tok, s.embed_ctx_budget, s.embed_batch_size)
```

(Keep the existing comment block explaining the pin, moved next to the Settings field; the per-request loop `for group in batches:` below is unchanged.)

**Tests:**
- `services/workers/tests/test_embedder_batches.py` (workers suite): `plan_batches` with a fake tokenizer (word-count × 2) — batches respect ctx_budget, oversized chunk is hard-cut to budget, `embed_batch_size` cap honored, budget from argument (not constant).
- `tests/test_config.py` (root): `Settings(embed_ctx_budget=8000, _env_file=None)` plumbs through; default is 1900.

**Suite:** workers (`services/workers/tests/`) + root for the config test.

---

## Phase 4 — R-13: window-level page citations

**Objective:** Each window cites its own first/last line's pages, not the section's.

**Edit `libs/chunking/src/chunking/hybrid.py`:** carry `(line_no, word)` pairs through the pack.

1. `_sections` returns `(heading_path, body_lines, ...)` where `body_lines: list[tuple[int, str]]` — `(global_line_no, text)` pairs instead of a joined `str` body (internal function; the only caller is `chunk_markdown`). Track line numbers alongside `current` (append `(line_no, line)`); `start_line`/`end_line` stay for the first/last section bound.
2. `chunk_markdown` pack loop:

```python
    for heading_path, body_lines, _start, _end in _sections(markdown):
        words = [(w, ln) for ln, line in body_lines for w in line.split()]
        if not words:
            continue
        window: list[tuple[str, int]] = []
        window_tokens = 0
        for word, line_no in words:
            t = count(word)
            if window and window_tokens + t > max_tokens:
                _emit(chunks, heading_path, window, count, pages)
                window, window_tokens = [], 0
            window.append((word, line_no))
            window_tokens += t
        if window:
            _emit(chunks, heading_path, window, count, pages, min_tokens=min_tokens)
```

3. `_emit(chunks, heading_path, window_words, count, pages=None, min_tokens=0)`:

```python
    text = " ".join(w for w, _ in window_words)
    if heading_path:
        text = "\n\n".join([heading_path[-1], text])
    if count(text) < min_tokens:
        return
    h = hashlib.sha256(" ".join(text.split()).encode("utf-8")).hexdigest()
    page_start = pages[window_words[0][1]] if pages else None
    page_end = pages[window_words[-1][1]] if pages else None
```

(R-10's section-level behavior is preserved for single-window sections — `tests/test_chunking.py:67-79` expectations still hold: first window starts at the first body word's line, last window ends at the section's last word. Text and hashes are unchanged; only page bounds tighten.)

**Tests** (`tests/test_chunking.py`, root suite): new `test_windows_carry_own_page_range` — a 10-line single-section body with `max_tokens` forcing 3 windows and a line→page map; assert the 3 chunks have strictly increasing `page_start`s and each `page_start < section_end` (previously all three shared the section range). Keep the two existing page tests green.

**Suite:** root (`tests/`).

---

## Phase 5 — Retry-ladder sub-sharding (M2, PRD §6.3)

**Objective:** Implement ladder attempts 2-4 in `handle_parse`: attempt 2 re-splits the shard into 4 sub-shards of `SHARD_PAGES/4`; attempt 3 into single pages; attempt ≥4 converts text-only (table structure off); final failure path unchanged (janitor escalation already handles attempts ≥ `max_shard_attempts`).

**Key design decisions (validated against code):**
- Attempt number = `shard.attempts` after `claim_shard` (repo.py:85 increments then returns).
- Sub-shards get **new `idx` values** (max idx per doc + 1 + i) and the embedder's shard query (`embedder.py:100-103`) switches `order_by(Shard.idx)` → `order_by(Shard.page_start)` so stitched page order stays correct regardless of idx. Normal books are unaffected (idx order == page_start order for `fixed_bounds`/`chapter_aligned_bounds`).
- The parent shard becomes `ShardState.SKIPPED` (exists in `ShardState`, models.py:55; embedder selects only `state=="done"`, so skipped parents are excluded from embed — correct).
- `Document.total_shards` must grow by the sub-shard count or `book_settled` (repo.py:145-149) never fires.
- Text-only attempt builds the converter with a settings copy: `s.model_copy(update={"parsing_do_table_structure": False})` — `converter_cache_key` already includes `parsing_do_table_structure` (converter.py:73), so the cache separates the variants for free.

**Edit `libs/core/src/core/db/repo.py`:** extend `insert_shards` with `start_idx: int = 0`:

```python
def insert_shards(session, doc_id, bounds, start_idx: int = 0) -> int:
    """Insert one row per shard bound; idx runs from start_idx (retry-ladder
    sub-shards continue after the parent's idx so idx stays unique per doc)."""
    session.add_all(
        [
            Shard(doc_id=doc_id, idx=start_idx + i, page_start=s, page_end=e)
            for i, (s, e) in enumerate(bounds)
        ]
    )
    return len(bounds)
```

plus a helper `next_shard_idx(session, doc_id) -> int` (`select(func.max(Shard.idx)).where(Shard.doc_id == doc_id)` + 1) and `skip_shard(session, doc_id, idx)` setting state SKIPPED.

**Edit `services/workers/src/workers/parser.py`:** after `claim_shard` returns a shard (line 55-58), before the PDF work:

```python
    # Retry ladder (PRD §6.3) — attempts are not identical. claim_shard
    # already incremented attempts, so shard.attempts IS this attempt's number.
    if shard.attempts >= 2:
        ladder = _ladder_action(shard, s)
        if ladder == "split":
            return _split_and_requeue(session, redis, doc, shard, s)
        # "text_only": fall through with a degraded converter config
```

with module functions:

```python
def _ladder_action(shard, s: Settings) -> str:
    """Attempt 2: quarter-split; attempt 3: single-page; attempt >=4:
    text-only convert. A shard too small to split skips straight to
    text-only. Return "split"|"text_only"."""
    span = shard.page_end - shard.page_start + 1
    if shard.attempts == 2 and span >= 4 * max(1, s.shard_pages // 4) // 2:  # worth quarter-splitting
        return "split"
    if shard.attempts == 3 and span >= 2:
        return "split"  # single-page sub-shards
    return "text_only"


def _split_and_requeue(session, redis, doc, shard, s: Settings) -> None:
    """Replace a failing shard with finer sub-shards (ladder attempts 2-3).
    The parent becomes SKIPPED; each sub-shard is a fresh ParseJob."""
    from core.queue import contracts, streams as st

    span = shard.page_end - shard.page_start + 1
    shard_pages = 1 if shard.attempts >= 3 else max(1, span // 4)
    bounds = [
        (shard.page_start + b.page_start - 1, shard.page_start + b.page_end - 1)
        for b in parsing_splitter_fixed_bounds(span, shard_pages, overlap=0)
        # overlap=0 is required: sub-shards must tile the parent disjointly
        # (fixed_bounds' default overlap=1 would emit overlapping bounds —
        # a span-20 attempt-2 shard would yield 5 overlapping bounds like
        # [1-5],[5-9],[9-13],... instead of 4 disjoint ones, double-counting
        # total_shards and re-parsing boundary pages).
    ]
    start_idx = repo.next_shard_idx(session, doc.id)
    repo.skip_shard(session, doc.id, shard.idx)
    repo.insert_shards(session, doc.id, bounds, start_idx=start_idx)
    doc.total_shards = (doc.total_shards or 0) + len(bounds)
    session.flush()
    for i, (ps, pe) in enumerate(bounds):
        st.xadd_job(
            redis, st.STREAM_PARSE,
            contracts.ParseJob(
                doc_id=doc.id, idx=start_idx + i, page_start=ps, page_end=pe,
                source_uri=doc.source_uri,
            ),
        )
    from core.events import write_event

    write_event(
        session, "warn", "parse", 
        f"shard {shard.idx} re-split into {len(bounds)} sub-shards "
        f"(ladder attempt {shard.attempts})",
        doc_id=doc.id, shard_idx=shard.idx, code="SHARD_RESPLIT",
    )
```

(`parsing_splitter_fixed_bounds` = `from parsing.splitter import fixed_bounds` — reused, offset by `page_start - 1`, and **always with `overlap=0`** so sub-shards tile the parent disjointly; the snippet above already passes it — `fixed_bounds` takes the param, splitter.py:22-25, default `overlap=1`. Attempt 4 / `text_only`: replace the `converter = get_converter(...)` call (parser.py:109) with `get_converter(need_ocr=verdict.needs_ocr, settings=s.model_copy(update={"parsing_do_table_structure": False}), builder=build_converter)` when the ladder says text_only and `s.parsing_do_table_structure` is still True.)

**Edit `services/workers/src/workers/embedder.py:100-103`:** `.order_by(Shard.idx)` → `.order_by(Shard.page_start)` (comment: sub-shards continue idx after the parent; page_start is the true document order).

**Tests `services/workers/tests/test_retry_ladder.py`** (workers suite; fake session/redis pattern of `test_embedder_retry_cap.py`, converter monkeypatched):
1. Attempt-2 shard (span 20, SHARD_PAGES=20) → parent SKIPPED, 4 sub-shard rows of 5 pages each, 4 `ParseJob` XADDs, `total_shards` +4, `SHARD_RESPLIT` event.
2. Attempt-3 shard (span 6) → 6 single-page sub-shards.
3. Attempt-4 → converter receives settings with `parsing_do_table_structure=False`; shard proceeds to normal done/fail path.
4. Small-span attempt-2 (span 2) → falls through to text_only, no split.
5. Sub-shards embed in page order: embedder query ordered by `page_start` (assert via captured statement or a fake-session ordering check).

**Suite:** workers (`services/workers/tests/`) for behavior; root suite unaffected except repo signature (root has no repo shard tests — verify `make test` stays green).

---

## Phase 6 — Metrics rollup writer (§10.1) + R-18 (embedded_at)

**Objective:** Janitor writes one `metrics_rollup` row per minute bucket from real data; embedder stamps `chunks.embedded_at`.

**Migration `migrations/versions/0004_shard_done_at.py`:** revision id `0004_shard_done_at`, `down_revision = "0003_api_key_expiry_usage"` — the chain is already `0001_initial → 0002_parsed_uri_md → 0003_api_key_expiry_usage` (verified: `migrations/versions/0003_api_key_expiry_usage.py:19-20`), so id 0002/0003 are taken and a new 0002 would break `alembic upgrade head`. Content: `op.add_column("shards", sa.Column("done_at", sa.DateTime(timezone=True), nullable=True))` (+ index `ix_shards_done_at`); downgrade drops both. (Needed because shards carry no completion timestamp — the rollup needs per-minute windows for pages/p50/p95.)

**Edit `libs/core/src/core/db/models.py` (Shard):** `done_at: Mapped[datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)`; add `MetricsRollup` model mirroring migration 0001:128-143 columns exactly (bucket PK TIMESTAMPTZ, pages_parsed, shards_done, shards_failed, chunks_embedded, parse_p50_ms, parse_p95_ms, peak_rss_p95_mb, queue_depth JSONB, search_p95_ms, search_count).

**Edit `libs/core/src/core/db/repo.py` (`mark_shard_done`):** set `shard.done_at = datetime.now(UTC)` alongside state DONE.

**Edit `services/workers/src/workers/embedder.py` (chunk insert, ~line 142):** add `embedded_at=func.now()` to the `pg_insert(Chunk).values(...)` — sets R-18's column on first insert (ON CONFLICT DO NOTHING keeps the original stamp on re-delivery, which is the desired semantics).

**Edit `services/workers/src/workers/janitor.py`:** module-level `_last_bucket: datetime | None = None` and a `write_metrics_rollup(session, redis, settings)` step run at the end of `janitor_pass` when the current minute bucket > `_last_bucket`:

```python
def write_metrics_rollup(session, redis, settings: Settings) -> bool:
    """One row per minute bucket (PRD §10.1): per-minute deltas from the
    previous bucket's snapshot + windowed p50/p95 from shards.done_at.
    search_p95_ms/search_count stay None for now — no search-latency
    capture exists yet (UsageMiddleware records counts, not durations)."""
    now = datetime.now(UTC)
    bucket = now.replace(second=0, microsecond=0)
    window_start = bucket - timedelta(minutes=1)
    rows = (
        session.execute(
            select(Shard).where(Shard.done_at.is_not(None), Shard.done_at >= window_start, Shard.done_at < bucket)
        ).scalars().all()
    )
    if not rows and _last_bucket == bucket:
        return False
    durations = sorted(r.duration_ms or 0 for r in rows)
    def pct(sorted_list, p):
        if not sorted_list:
            return None
        i = max(0, round((len(sorted_list) - 1) * p / 100))
        return sorted_list[i]
    queue_depth = {name: streams.queue_depth(redis, name) for name in streams.ALL_STREAMS}  # try/except → {}
    from core.db.models import MetricsRollup

    session.merge(MetricsRollup(
        bucket=bucket,
        pages_parsed=sum(r.page_end - r.page_start + 1 for r in rows),
        shards_done=len(rows),
        shards_failed=sum(1 for r in rows if r.state == "failed"),
        chunks_embedded=...,  # count(Chunk) where embedded_at in window — same pattern
        parse_p50_ms=pct(durations, 50),
        parse_p95_ms=pct(durations, 95),
        peak_rss_p95_mb=pct(sorted(r.peak_rss_mb or 0 for r in rows), 95),
        queue_depth=queue_depth,
    ))
    return True
```

(`session.merge` = idempotent upsert on the bucket PK; `_last_bucket` updated on success — janitor is a single process, restart just re-writes one bucket. Wire as step 7 in `janitor_pass` before the return; add `"rollup": 0/1` to the counters dict.)

**Tests:**
- `services/workers/tests/test_metrics_rollup.py` (workers suite): fake session capturing the merged object + canned shards in-window → assert pages_parsed sum, p50/p95 by index, bucket floor to the minute, second call in the same minute is a no-op.
- Root `tests/` : none needed beyond existing suites staying green (model is exercised via the workers test).

**Suite:** workers (`services/workers/tests/`).

---

## Phase 7 — Minor fixes (README, read-endpoint auth, eval key bug, `/pipeline` swallow, prefix A/B toggle)

### 7a. README + CLAUDE.md drift (minor 1)
**Edit `README.md:56-57`:** replace the two phantom targets with the real ones:

```markdown
make up-ingest    # parsers run as discrete replicas (worker-parser..worker-parser-4)
make down-ingest  # docker compose down on the ingest profile — stops consumers immediately
```

(The `down-ingest` comment must describe what the target does: `docker compose --profile ingest down` kills consumers immediately. "Let in-flight shards finish" is `drain` semantics — a target that does not exist — and must not be documented as `down-ingest` behavior. If drain semantics are wanted later, implement `make drain` explicitly; not in scope here.)

**Edit `CLAUDE.md:31-32` in the same commit:** the Commands section documents the same phantom `make scale N=8` and `make drain` targets (the Makefile has neither — fixing only README.md leaves the drift exactly where future sessions trip on it). Replace both lines with the real targets, matching the README wording:

```markdown
make up-ingest   # parsers run as discrete replicas (worker-parser..worker-parser-4)
make down-ingest # docker compose down on the ingest profile — stops consumers immediately
```

While in README.md, also correct the stale Status section (README.md:90-95 — still says rerank is stubbed; it is implemented per `db1ffa9`/`6c5b5b1`).

### 7b. Read-endpoint auth (minor 2)
**Edit `services/api/src/api/routers/documents.py`:** add `key=Depends(require_scope("search"))` to `list_documents` (:92), `get_document` (:111), `get_shards` (:121); **edit `collections.py:22`** likewise for `list_collections`.

**Required companion (or the admin UI breaks) — route the gated browser calls through the existing session-gated proxy.** The pages consuming the newly-gated endpoints are **client components** fetching from the browser via the same-origin rewrite, which carries **no header**: `services/web/app/(dashboard)/documents/page.tsx:1` is `"use client"` and fetches via `useDocuments()` → `/v1/documents` (same for `collections/page.tsx:1` → `/v1/collections`, and detail pages via `useDocument`/`useShards` in `services/web/lib/queries.ts`). Those calls will 401 the moment `require_scope("search")` lands. Fix, per call path:

1. **Browser calls to gated endpoints** (`useDocuments`, `useDocument`, `useShards`, `useCollections`): change the generated-SDK call sites to go through the existing session-gated same-origin proxy `/api/admin/v1/*` (handler: `services/web/app/api/admin/[...path]/route.ts`, which verifies the session cookie then attaches `Authorization: Bearer $API_ADMIN_KEY` upstream). Concretely: edit `services/web/lib/api-client.ts` so `documents`, `document`, `shards` and `collections` hit the `/api/admin/v1/...` paths in the browser (keep the direct `/v1/...` paths when `!isBrowser`, where server components can use `API_URL` with a server-held key). The proxy already supports GET (`route.ts:35-37`) — no new proxy code needed.
2. **`API_ADMIN_KEY` scope:** document in `.env.example` that `API_ADMIN_KEY` must carry **both** `admin` and `search` scopes (`VALID_SCOPES` in `services/mcp/src/mcp_server/auth.py:15` — no superset logic exists in `require_scope`, deps.py:74-78, so the key needs both), or every proxied dashboard read 403s.
3. **Server components** that read these endpoints keyless via `API_URL` (`services/web/lib/oid-client.ts:12-14` wiring) keep working only if their callers are covered by the browser-path change or converted — audit each `apiClient.*` caller during implementation; any remaining server-side reader gets the bearer header attached server-side where `API_ADMIN_KEY` is already available.

Verification: `npm run build` in `services/web/` must pass, and the documents + collections pages must render logged-in (they are the pages that break if step 1 is skipped).

**Test** (root `tests/test_read_endpoints_auth.py`): introspect `documents.router.routes` / `collections.router.routes` — every GET route's dependant declares a `require_scope` dependency; no 401-bypass remains. (Web-side proxy routing is covered by the `services/web` build + manual page check, per repo convention — no Playwright lane exists.)

### 7c. Eval hit@top_k (R-19, minor 6)
**Edit `scripts/eval/judge.py` (CategoryStats):** add property

```python
    @property
    def hit_at_top_k(self) -> float:
        """Any-rank hit rate: a rank was recorded iff the expected doc
        appeared within the run's top_k (reciprocal_ranks gets one entry
        per hit, misses append nothing) — correct for any top_k, unlike
        the fixed hit@1/3/8 counters."""
        return len(self.reciprocal_ranks) / self.queries if self.queries else 0.0
```

**Edit `scripts/eval/run.py:214`:** `f"hit_at_{args.top_k}": summary.overall.hit_at_8,` → `"hit_at_top_k": summary.overall.hit_at_top_k,`; same for the console print at :239 (`hit@k={overall.hit_at_top_k:.2f}`). Check `scripts/eval/compare.py` for consumers of the old key during implementation and update its reader (keep a backwards-compat fallback for old result files).

**Test** (`tests/test_eval_judge.py`, root suite): Summary with 3 queries, 2 hits (ranks 2 and 5) → `hit_at_top_k == pytest.approx(2/3)` while `hit_at_8 == 2/3` and `hit_at_1 == 1/3`; a top_k=16-shaped case where rank 12 counts for top_k but not for hit@8.

### 7d. `/pipeline` lane errors (R-20, minor 7)
**Edit `services/api/src/api/routers/system.py:160-161`:**

```python
        except Exception as exc:
            # Mirror /queues' stance: a dead Redis must not read as an
            # empty lane silently. waiting=None already signals unknown;
            # make the cause explicit and visible in logs (R-20).
            logger.warning("pipeline lane read failed for %s: %s", name, exc)
            lane["error"] = str(exc)
```

(add `logger = logging.getLogger(__name__)` at module top; keep the `embed_backend` line-128 swallow as-is — it already surfaces `"down"` explicitly.)

**Test** (`tests/test_system_queues.py`, root suite, existing fake-redis pattern): redis raising on `xinfo_groups` → `/pipeline` lane dict carries `error`, function does not raise, other lanes still populated.

### 7e. Query-prefix A/B toggle (minor 5)
**Edit `libs/core/src/core/config.py`:** `embed_query_prefix: str = Field(default="search_query: ", description="asymmetric-query prefix; empty disables (bge-m3 docs specify none — A/B via scripts/eval)")`.
**Edit `services/api/src/api/routers/search.py:40`** → `qclient.embed([f"{s.embed_query_prefix}{body.query}"])[0]`; **edit `services/mcp/src/mcp_server/server.py:54`** — the prefix is applied inside `search_impl`, which already receives `settings: Settings` as a parameter — so the edit is directly at that line:

```python
    dense = query_embedder(f"{settings.embed_query_prefix}{query}")
```

(Not in the tool wiring: the embedder lambda at server.py:274 only passes the callable through; the `search_query:` prefix lives at server.py:54 inside `search_impl`.)

**Acceptance (operational, no unit test beyond config):** with the stack up, run `uv run python -m scripts.eval.run --label prefix-on --top_k 8 ...` and `EMBED_QUERY_PREFIX=""` `--label prefix-off`, compare via `scripts/eval/compare.py`; commit the winning default and record both result files under `scripts/eval/results/` (existing convention, cf. commit `937b9a1`). Unit tests: config field default present (root `test_config.py`).

---

# VERIFICATION (whole plan)

```bash
uv sync --locked --group dev --all-packages        # after pyproject changes (Phase 3a)
make test                                          # root suite: retry router, point ids, hydration,
                                                   #   build_filter, paged upsert, commit router, chunking
                                                   #   pages, config, eval judge, system queues, auth introspection
uv run pytest services/workers/tests/ -q           # workers suite: plan_batches, retry ladder, metrics rollup
uv run pytest tests/ services/workers/tests/ -q    # everything (~269 + new)
make lint                                          # uvx ruff check . (line-length 100)
```

Per-phase suite mapping: Phases 1, 2, 3a, 4, 7b-7d → root `tests/`; Phases 3b, 5, 6 → `services/workers/tests/` (+ root for config); config-field additions → root `tests/test_config.py`.

Deploy/operational notes for the implementer (not CI-verifiable):
- Phase 1b: run `uv run python -m scripts.reindex` once after deploy to re-derive point ids (Qdrant is a cache, PRD §13.5).
- Phase 5: flip `docs/BACKLOG.md` R-11..R-21 rows to `fixed <sha>` as each lands; parser docstring TODO M2 lines (parser.py:12-14) get deleted in Phase 5; CLAUDE.md's "Still stubbed" paragraph updates in the same commit.
- Phase 7b: confirm `API_ADMIN_KEY` carries `["admin","search"]` scopes and that the browser calls to `/v1/documents`/`/v1/collections` now ride the `/api/admin` proxy — the documents, collections, and document-detail pages must render logged-in (web build passes; manual check of `/`, `/documents`, `/collections`, `/pipeline` pages).
