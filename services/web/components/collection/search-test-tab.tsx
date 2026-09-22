"use client";

/** KB Search Test tab (WeKnora Tab 3): pinned single-collection retrieval
 * test — query input, top_k, rerank toggle, score list with matched-chunk
 * expansion. Hit styling follows the playground's ResponseViewer patterns
 * (mono scores, bordered result rows). */

import { cn } from "cn";
import { ChevronDown, ChevronRight, Search } from "lucide-react";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import type { SearchHitRow } from "@/lib/api-client";
import { apiClient } from "@/lib/api-client";

function HitRow({ hit, index }: { hit: SearchHitRow; index: number }) {
  const [open, setOpen] = useState(false);
  const breadcrumb = (hit.heading_path ?? []).join(" > ");
  return (
    <div className="rounded border border-sheet-edge bg-sheet">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-start gap-2.5 px-3 py-2.5 text-left"
        aria-expanded={open}
      >
        <span className="mt-0.5 shrink-0 text-muted-foreground">
          {open ? (
            <ChevronDown className="size-4" aria-hidden="true" />
          ) : (
            <ChevronRight className="size-4" aria-hidden="true" />
          )}
        </span>
        <span className="min-w-0 flex-1">
          <span className="flex flex-wrap items-baseline gap-x-2.5 gap-y-0.5">
            <span className="font-mono text-[12.5px] font-semibold text-press tabular-nums">
              #{index + 1} {hit.score.toFixed(4)}
            </span>
            {hit.reranked && (
              <span className="rounded-[2px] border border-press px-1 font-mono text-[0.65rem] font-semibold text-press-deep">
                rerank
              </span>
            )}
            {hit.partial && (
              <span className="rounded-[2px] border border-warning px-1 font-mono text-[0.65rem] font-semibold text-warning">
                partial
              </span>
            )}
            <span className="min-w-0 truncate font-medium">{hit.doc_title ?? hit.doc_id}</span>
            {hit.page_start != null && (
              <span className="font-mono text-[11.5px] text-muted-foreground">
                p.{hit.page_start}
                {hit.page_end != null && hit.page_end !== hit.page_start ? `–${hit.page_end}` : ""}
              </span>
            )}
          </span>
          {breadcrumb && (
            <span className="mt-0.5 block truncate font-mono text-[11px] text-muted-foreground">
              {breadcrumb}
            </span>
          )}
          <span
            className={cn(
              "mt-1 block text-[12.5px] text-muted-foreground",
              open ? "whitespace-pre-wrap break-words" : "line-clamp-2",
            )}
          >
            {hit.text}
          </span>
        </span>
      </button>
    </div>
  );
}

export function SearchTestTab({ collectionId }: { collectionId: string }) {
  const [query, setQuery] = useState("");
  const [topK, setTopK] = useState(8);
  const [rerank, setRerank] = useState(false);
  const [running, setRunning] = useState(false);
  const [hits, setHits] = useState<SearchHitRow[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function run(e: React.FormEvent) {
    e.preventDefault();
    if (!query.trim()) return;
    setRunning(true);
    setError(null);
    try {
      const res = await apiClient.search({
        query: query.trim(),
        collection: collectionId,
        top_k: topK,
        rerank,
      });
      setHits(res);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setRunning(false);
    }
  }

  return (
    <div className="flex flex-col gap-3">
      <Card>
        <CardContent className="flex flex-col gap-3">
          <form onSubmit={run} className="flex flex-wrap items-end gap-3">
            <div className="flex min-w-[240px] flex-1 flex-col gap-1.5">
              <Label htmlFor="st-query" className="text-xs font-semibold text-muted-foreground">
                test query
              </Label>
              <div className="relative">
                <Search
                  className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground"
                  aria-hidden="true"
                />
                <Input
                  id="st-query"
                  placeholder="ask this knowledge base a question…"
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  className="pl-8"
                />
              </div>
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="st-topk" className="text-xs font-semibold text-muted-foreground">
                top_k
              </Label>
              <Input
                id="st-topk"
                type="number"
                min={1}
                max={50}
                value={topK}
                onChange={(e) => setTopK(Number(e.target.value) || 8)}
                className="w-20"
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label className="text-xs font-semibold text-muted-foreground">rerank stage</Label>
              <div className="flex h-9 items-center gap-2">
                <Switch checked={rerank} onCheckedChange={setRerank} aria-label="rerank toggle" />
                <span className="text-[12px] text-muted-foreground">
                  {rerank ? "cross-encoder re-scoring on" : "RRF order only"}
                </span>
              </div>
            </div>
            <Button type="submit" disabled={running || !query.trim()}>
              {running ? "Searching…" : "Test"}
            </Button>
          </form>
          <p className="m-0 text-[11.5px] text-muted-foreground">
            Scores are fused RRF ranks (dense 1.0 / BM25 0.3, k=60)
            {rerank
              ? " re-ordered by the bound reranker; failures degrade to RRF order."
              : ". Bound the reranker in the Settings tab to enable the toggle's effect."}
          </p>
        </CardContent>
      </Card>

      {error && (
        <Card>
          <CardContent>
            <p className="text-redink">search failed: {error}</p>
          </CardContent>
        </Card>
      )}

      {hits && (
        <div className="flex flex-col gap-2">
          <p className="m-0 text-[11.5px] font-semibold tracking-[0.02em] text-muted-foreground">
            {hits.length} hit{hits.length === 1 ? "" : "s"} — sorted by score
          </p>
          {hits.map((h, i) => (
            <HitRow key={h.chunk_id} hit={h} index={i} />
          ))}
          {hits.length === 0 && (
            <Card>
              <CardContent className="py-6 text-center text-muted-foreground">
                no chunks matched this query
              </CardContent>
            </Card>
          )}
        </div>
      )}
    </div>
  );
}
