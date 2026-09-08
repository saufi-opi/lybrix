"use client";

/** Honest progress (PRD §8.2): parse counts as 75%, embed as 25%; the
 * phase name rides alongside, never a fake percentage. */

export function ProgressBar({ pct, label }: { pct: number; label?: string }) {
  return (
    <div>
      <div className="progress">
        <div style={{ width: `${Math.min(100, Math.max(0, pct))}%` }} />
      </div>
      {label ? <small className="muted">{label}</small> : null}
    </div>
  );
}
