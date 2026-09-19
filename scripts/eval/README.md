# lybrix Retrieval Evaluation Harness

Golden-set eval harness for retrieval quality, built **before** Phase 1
(Qdrant native-BM25 hybrid activation) and Phase 2 (TEI cross-encoder
reranker) so every uplift is measured against the same yardstick.

Pure Python, stdlib only, zero new dependencies, zero new infrastructure.
Runs from VM1 against the live MCP endpoint. All commands below only work
**from repo root** (pytest's prepend import mode / `python -m` cwd put the
repo root on `sys.path`); there is deliberately no sys.path hack.

## Setup

```bash
uv sync --locked --group dev --all-packages   # refresh the root venv first
export LYBRIX_MCP_URL=http://100.109.176.118:8430/mcp   # Tailscale endpoint
export LYBRIX_MCP_TOKEN=<token>               # env only, never hardcoded
```

The public CF-fronted alternative `https://lybrix.loxikum.xyz/mcp` also works
(both verified live 2026-09-18); the harness sends a custom User-Agent
because `Python-urllib/*` is bot-blocked on the public URL.

## Run

```bash
uv run python -m scripts.eval.run \
  --dataset scripts/eval/datasets/seed.jsonl \
  --top-k 8 --label baseline --junk-filter on
```

- `--collection <id-or-name>` is **optional** (default: omit → no collection
  filter → full corpus). When supplied, the value is resolved against
  `list_collections`: a value matching a collection **id** is sent as-is, a
  value matching a **name** is resolved to that collection's id before any
  search call (the MCP `search` tool's `collection` arg filters the Qdrant
  payload field `collection_id`, which carries the Collection id — a name
  that differs from its id would silently filter every query to zero rows).
  A value matching neither exits with the valid ids and names listed.
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
uv run python -m scripts.eval.compare scripts/eval/results/baseline-*.json \
                                       scripts/eval/results/phase1-*.json
```

Matches queries by `id`; prints aggregate deltas (hit@1/3/8, MRR) and the
per-query regression list (rank got worse or a hit was lost) plus
improvements. Exit 0 either way; `--fail-on-regression` gates for CI later.

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
- The loader raises `DatasetError` with line numbers on duplicate ids,
  unknown categories, or missing/empty `expected_doc_titles`. It also
  rejects expected titles normalizing to < 8 space-stripped chars (they can
  never match — see `judge.titles_match`).
- **Contract: a placeholder row is never committed.** Fill every row from
  real corpus verification (`list_documents` / `read_pages`) before commit,
  and record in each row's `notes` which probe verified its titles.

### Seed dataset

`datasets/seed.jsonl` ships ~15 SEED pairs; every row's `notes` starts with
`SEED:` and says which live probe verified its titles. Stratification:

- **exact** — exact terms/code identifiers where dense-only retrieval
  historically loses and BM25 (Phase 1) should help. These are the rows that
  move when Phase 1 lands.
- **conceptual** — natural-language questions; the dense channel baseline;
  guards against Phase 1 *hurting* conceptual recall.
- **drift** — generic multi-keyword queries known to drift (generic terms
  pull cross-domain junk). Where junk-heading/TOC overmatch currently
  masquerades as a hit; Phases 1–2 should move them.

Extend to 50–100 pairs by appending rows in the same format.

## Tests

```bash
uv run pytest tests/test_eval_client.py tests/test_eval_judge.py -q
```

No network: the client is tested through an injected fake transport, and the
judge/filters/dataset/compare logic is pure functions on plain dicts.

## Score channel note

Live probes showed search scores saturate at RRF ranks (0.5 / 0.333 / 0.25) —
the score channel is unusable for tuning. This harness therefore measures
*rank* metrics only (hit@k, MRR), never score deltas.
