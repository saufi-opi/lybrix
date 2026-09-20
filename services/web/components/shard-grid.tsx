"use client";

/** Shard grid — the most useful widget in the product (PRD §8.1).
 * One cell per shard, coloured by state; tooltip carries page range,
 * attempts, duration, peak RSS. A red cell at shard 14 tells the
 * operator exactly where to look in one glance. */

export function ShardGrid({
  shards,
  onSelect,
}: {
  shards: {
    idx: number;
    page_start: number;
    page_end: number;
    state: string;
    attempts: number;
    duration_ms: number | null;
    peak_rss_mb: number | null;
    error_code: string | null;
  }[];
  onSelect?: (idx: number) => void;
}) {
  return (
    <div className="shard-grid">
      {shards.map((s) => (
        <button
          type="button"
          key={s.idx}
          className={`shard-cell ${s.state}`}
          title={`shard ${s.idx} · pages ${s.page_start}–${s.page_end} · ${s.state} · attempts ${s.attempts}${
            s.duration_ms ? ` · ${(s.duration_ms / 1000).toFixed(1)}s` : ""
          }${s.peak_rss_mb ? ` · rss ${s.peak_rss_mb}MB` : ""}${
            s.error_code ? ` · ${s.error_code}` : ""
          }`}
          onClick={() => onSelect?.(s.idx)}
        />
      ))}
    </div>
  );
}
