"use client";

/** Honest progress (PRD §8.2): parse counts as 75%, embed as 25%; the
 * phase name rides alongside, never a fake percentage. */

export function ProgressBar({ pct, label }: { pct: number; label?: string }) {
  return (
    <div>
      <div className="h-2 overflow-hidden rounded-[2px] border border-sheet-edge bg-paper-deep">
        <div
          className="h-full bg-press transition-[width] duration-400 ease-out"
          style={{ width: `${Math.min(100, Math.max(0, pct))}%` }}
        />
      </div>
      {label ? <small className="text-xs text-muted-foreground">{label}</small> : null}
    </div>
  );
}
