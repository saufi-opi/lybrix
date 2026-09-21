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
| R-10 | P2 | fixed this round | citations show no pages (chunks.page_start/end NULL) | stitch drops page metadata; chunking never sets it | this round |
| R-11 | P1 | fixed 2026-09-21 (f1f0415) | retry `scope=shards` resets shards to pending but enqueues an embed job the embedder ignores → failed shards never re-parsed; doc flips ready/partial with a stranded pending shard | no ParseJob enqueued; embedder selects `state=="done"` only | f1f0415 |
| R-12 | P0 | fixed 2026-09-21 (f1f0415) | second document's chunks overwrite first document's Qdrant points; citations can attribute the wrong book | point id derived from chunk_hash alone; UNIQUE(doc_id, chunk_hash) is composite; hydrate keys rows by chunk_hash only | f1f0415 |
| R-13 | P3 | fixed 2026-09-21 (dba6f06) | all windows of a long section cite the section's full page span | `_emit` indexes the page map with the section's line bounds, not the window's | dba6f06 |
| R-14 | P2 | fixed 2026-09-21 (7374de4) | API key scoped to collection A returns < top_k when B-chunks fill slots | key-level collection scope post-filters after Qdrant top_k truncation (MCP path pushes down) | 7374de4 |
| R-15 | P2 | fixed 2026-09-21 (94c4484) | dedupe key and stored hash are client-controlled | commit stores `body.content_sha256` without reading the object (PRD §6.1 says API computes it) | 94c4484 |
| R-16 | P3 | fixed 2026-09-21 (94c4484) | TEI embed path batches at a budget pinned to ollama's 2048 ceiling; tokenizer reloaded per job | `ctx_budget = 1900` + `from_pretrained("BAAI/bge-m3")` hardcoded in handle_embed | 94c4484 |
| R-17 | P2 | fixed 2026-09-21 (7374de4) | one ~12MB blocking upsert per book re-creates the 2026-09-11 timeout-loop shape | `upsert_chunks` sends all points in a single `wait=True` request | 7374de4 |
| R-18 | P3 | fixed 2026-09-21 (39e00cf) | `chunks.embedded_at` never stamped; re-embed tooling cannot identify stale vectors | embedder insert omits the column | 39e00cf |
| R-19 | P3 | fixed 2026-09-21 (c3893cc) | eval JSON reports `hit_at_{top_k}` carrying the hit@8 value; undercounts for top_k > 8 | run.py conflates judge's fixed hit@8 with run top_k | c3893cc |
| R-20 | P3 | fixed 2026-09-21 (c3893cc) | `/pipeline` shows a dead Redis as empty lanes while `/queues` propagates | lanes loop `except Exception: pass` | c3893cc |
| R-21 | P2 | fixed 2026-09-21 (94c4484) | non-PDF / over-cap PDFs are discovered inside a parser instead of rejected at commit | no magic-byte / page-count validation (PRD §11) | 94c4484 |
