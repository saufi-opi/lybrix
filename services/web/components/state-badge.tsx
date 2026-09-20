"use client";

/** State stamps — filled ink, paper text (PRD §8.1). Maps a domain state to
 * a Badge variant + paper & press colors: ready = press (the one loud color),
 * failed = redink, partial = ochre, in-flight = ledger, unknown = muted. */

import { cn } from "cn";
import { Badge } from "@/components/ui/badge";

const STATE_STYLE: Record<string, { className: string }> = {
  // terminal — press green is the only loud color
  ready: { className: "border-press bg-press text-sheet" },
  active: { className: "border-press bg-press text-sheet" },
  ok: { className: "border-press bg-press text-sheet" },
  // failure / revoked — red ink
  failed: { className: "border-redink bg-redink text-sheet" },
  revoked: { className: "border-redink bg-redink text-sheet" },
  expired: { className: "border-redink bg-redink text-sheet" },
  down: { className: "border-redink bg-redink text-sheet" },
  // degraded — ochre
  partial: { className: "border-warning bg-warning text-sheet" },
  expiring: { className: "border-warning bg-warning text-sheet" },
  degraded: { className: "border-warning bg-warning text-sheet" },
  // in-flight — ledger blue on ledger wash
  uploaded: { className: "border-ledger bg-ledger-wash text-ledger" },
  splitting: { className: "border-ledger bg-ledger-wash text-ledger" },
  parsing: { className: "border-ledger bg-ledger-wash text-ledger" },
  embedding: { className: "border-ledger bg-ledger-wash text-ledger" },
  indexing: { className: "border-ledger bg-ledger-wash text-ledger" },
};

export function StateBadge({ state, className }: { state: string; className?: string }) {
  const style = STATE_STYLE[state] ?? { className: "bg-muted text-muted-foreground" };
  return (
    <Badge
      className={cn("rounded font-mono text-[0.72rem] font-semibold", style.className, className)}
    >
      {state}
    </Badge>
  );
}
