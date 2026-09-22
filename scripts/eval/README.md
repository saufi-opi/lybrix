# lybrix Retrieval Evaluation Harness (2.0 — Go)

Golden-set eval harness for retrieval quality. The 1.0 Python harness
(`scripts/eval/*.py`) is superseded by the Go command `lybrix-eval`; the
golden set (`datasets/seed.jsonl`) and this results directory are unchanged.

Runs from any host that can reach the live MCP endpoint (streamable HTTP,
bearer key). All metrics are rank-based (hit@k, MRR) — the score channel
saturates at RRF ranks and is unusable for tuning.

## Setup

```bash
go build -o bin/lybrix-eval ./cmd/lybrix-eval
export LYBRIX_MCP_URL=http://100.109.176.118:8430/mcp   # Tailscale endpoint
export LYBRIX_MCP_TOKEN=<token>               # env only, never hardcoded
```

The public CF-fronted alternative `https://lybrix.loxikum.xyz/mcp` also
works; the harness always sends a `lybrix-eval/0.1.0` User-Agent (the public
URL bot-blocks default client UA strings).

## Run

```bash
./bin/lybrix-eval run \
  --dataset scripts/eval/datasets/seed.jsonl \
  --top-k 8 --label baseline --junk-filter on
```

- `--collection <id-or-name>` is **optional** (default: omit → no collection
  filter → full corpus). When supplied, the value is resolved against the
  MCP `list_collections` tool: a value matching a collection **id** is sent
  as-is, a value matching a **name** is resolved to that collection's id
  before any search call (the MCP `search` tool's `collection` arg filters
  the `collection_id` payload field, which carries the Collection id — a
  name that differs from its id would silently filter every query to zero
  rows). A value matching neither exits with the valid ids and names listed.
- 0.3 s pacing between queries; ~15 queries run in ~10 s.
- Writes `scripts/eval/results/<label>-<UTC timestamp>.json` (per-query
  records, the compare input) and `.md` (human summary: hit@1/3/8, MRR,
  per-category table, junk-filter impact, per-query one-liners).
- Exits non-zero if any query raised, or when <50% of queries return ANY
  result rows (endpoint misconfig / auth / collection typo). Low hit-rate is
  a *finding*, not a crash — it never changes the exit code.
- `--junk-filter off` keeps TOC/junk-heading results in the metrics; the
  summary always reports raw top-8 junk rates too, so one run quantifies the
  filter's impact itself.

## Compare A vs B

```bash
./bin/lybrix-eval compare scripts/eval/results/baseline-*.json \
                          scripts/eval/results/phase1-*.json
```

Matches queries by `id`; prints aggregate deltas (hit@1/3/top_k/8, MRR) and
the per-query regression list (rank got worse or a hit was lost) plus
improvements. Exit 0 either way; `--fail-on-regression` gates for CI.

## Dataset format

JSONL, one query per line:

```json
{"id": "exact-001", "query": "reciprocal rank fusion hybrid search",
 "expected_doc_titles": ["Developing Apps with GPT-4 and ChatGPT"],
 "expected_headings": ["Reciprocal Rank Fusion (RRF)"],
 "category": "exact", "notes": "RRF section; score channel saturated"}
```

- `id` (required, unique), `query` (required), `expected_doc_titles`
  (required, non-empty list — hit if ANY matches), `expected_headings`
  (optional list — bonus check against `" › ".join(heading_path)`),
  `category` (required: `exact` | `conceptual` | `drift`), `notes` (free text).
- The loader errors with line numbers on duplicate ids, unknown categories,
  or missing/empty `expected_doc_titles`. It also rejects expected titles
  normalizing to < 8 space-stripped chars (they can never match).
- **Contract: a placeholder row is never committed.** Fill every row from
  real corpus verification (`list_documents` / `read_pages`) before commit,
  and record in each row's `notes` which probe verified its titles.

### Seed dataset

`datasets/seed.jsonl` ships 15 SEED pairs; every row's `notes` starts with
`SEED:` and says which live probe verified its titles. Stratification:

- **exact** — exact terms/code identifiers where dense-only retrieval
  historically loses and BM25 (pg_search, 2.0) should help. These are the
  rows that move when the hybrid weights are tuned.
- **conceptual** — natural-language questions; the dense channel baseline;
  guards against BM25 weight changes *hurting* conceptual recall.
- **drift** — generic multi-keyword queries known to drift (generic terms
  pull cross-domain junk). Where junk-heading/TOC overmatch currently
  masquerades as a hit.

Extend to 50–100 pairs by appending rows in the same format.

## Tests

```bash
go test ./cmd/lybrix-eval/
```

No network: the client is tested through an injected fake transport, and the
judge/filters/dataset/compare logic is pure functions on plain maps.
