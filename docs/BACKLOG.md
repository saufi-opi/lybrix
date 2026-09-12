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
| R-8 | P2 | open | vanished-doc parse jobs redeliver forever | runner docstring promises DLQ; none implemented; reclaim re-adds poison entries | — |
| R-9 | P3 | open | embedder fetches shard JSONs serially | per-shard get_object loop (embedder.py:79-84); embed not bottleneck | — |
| R-10 | P2 | fixed this round | citations show no pages (chunks.page_start/end NULL) | stitch drops page metadata; chunking never sets it | this round |
