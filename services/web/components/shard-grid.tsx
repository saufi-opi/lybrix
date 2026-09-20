"use client";

/** Shard grid — the most useful widget in the product (PRD §8.1).
 * One cell per shard, coloured by state; the shadcn tooltip carries page
 * range, attempts, duration, peak RSS. A red cell at shard 14 tells the
 * operator exactly where to look in one glance. */

import { cn } from "cn";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";

interface Shard {
  idx: number;
  page_start: number;
  page_end: number;
  state: string;
  attempts: number;
  duration_ms: number | null;
  peak_rss_mb: number | null;
  error_code: string | null;
}

function cellClass(state: string): string {
  switch (state) {
    case "done":
      return "border-press-deep bg-press";
    case "failed":
      return "border-redink bg-redink";
    case "running":
      return "border-ledger bg-ledger";
    default:
      return "border-sheet-edge bg-paper-deep";
  }
}

function shardTip(s: Shard): string {
  return (
    `shard ${s.idx} · pages ${s.page_start}–${s.page_end} · ${s.state} · attempts ${s.attempts}` +
    (s.duration_ms ? ` · ${(s.duration_ms / 1000).toFixed(1)}s` : "") +
    (s.peak_rss_mb ? ` · rss ${s.peak_rss_mb}MB` : "") +
    (s.error_code ? ` · ${s.error_code}` : "")
  );
}

export function ShardGrid({
  shards,
  onSelect,
}: {
  shards: Shard[];
  onSelect?: (idx: number) => void;
}) {
  return (
    <TooltipProvider delayDuration={150}>
      <div className="grid grid-cols-[repeat(auto-fill,minmax(28px,1fr))] gap-[3px]">
        {shards.map((s) => (
          <Tooltip key={s.idx}>
            <TooltipTrigger asChild>
              <button
                type="button"
                className={cn("aspect-square rounded-[2px] border", cellClass(s.state))}
                aria-label={shardTip(s)}
                onClick={() => onSelect?.(s.idx)}
              />
            </TooltipTrigger>
            <TooltipContent className="font-mono text-[11px]">{shardTip(s)}</TooltipContent>
          </Tooltip>
        ))}
      </div>
    </TooltipProvider>
  );
}
