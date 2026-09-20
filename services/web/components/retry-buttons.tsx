"use client";

/** The three retry buttons (PRD §8.3): they cost very different amounts
 * — retry failed shards = seconds, re-embed = ~30s, reprocess = minutes.
 * Never offer only "retry". Feedback via sonner toasts. */

import { useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";

export function RetryButtons({ docId }: { docId: string }) {
  const [busy, setBusy] = useState<string | null>(null);

  async function run(scope: string, label: string) {
    setBusy(scope);
    try {
      const res = await fetch(`/api/admin/v1/documents/${docId}/retry`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ scope }),
      });
      if (!res.ok) throw new Error(`API ${res.status}: ${(await res.text()).slice(0, 200)}`);
      toast.success(`${label} queued`);
    } catch (e) {
      toast.error(`${label} failed`, {
        description: e instanceof Error ? e.message : String(e),
      });
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="flex flex-wrap items-center gap-2">
      <Button
        type="button"
        variant="outline"
        size="sm"
        disabled={busy !== null}
        onClick={() => run("shards", "Retry failed shards")}
      >
        Retry failed shards
      </Button>
      <Button
        type="button"
        variant="outline"
        size="sm"
        disabled={busy !== null}
        onClick={() => run("embed", "Re-embed")}
      >
        Re-embed
      </Button>
      <Button
        type="button"
        variant="outline"
        size="sm"
        disabled={busy !== null}
        onClick={() => run("full", "Reprocess")}
      >
        Reprocess from scratch
      </Button>
      {busy && <span className="text-muted-foreground">working…</span>}
    </div>
  );
}
