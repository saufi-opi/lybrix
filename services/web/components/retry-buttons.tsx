"use client";

/** The three retry buttons (PRD §8.3): they cost very different amounts
 * — retry failed shards = seconds, re-embed = ~30s, reprocess = minutes.
 * Never offer only "retry". */

import { useState } from "react";
import { apiClient } from "@/lib/api-client";

export function RetryButtons({ docId }: { docId: string }) {
  const [busy, setBusy] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);

  async function run(scope: string, label: string) {
    setBusy(scope);
    setMsg(null);
    try {
      await apiClient.retry(docId, scope);
      setMsg(`${label} queued`);
    } catch (e) {
      setMsg(`failed: ${e instanceof Error ? e.message : String(e)}`);
    } finally {
      setBusy(null);
    }
  }

  return (
    <p>
      <button onClick={() => run("shards", "Retry failed shards")}>Retry failed shards</button>{" "}
      <button onClick={() => run("embed", "Re-embed")}>Re-embed</button>{" "}
      <button onClick={() => run("full", "Reprocess")}>Reprocess from scratch</button>{" "}
      {busy && <span className="muted">working…</span>}
      {msg && <span className="muted">{msg}</span>}
    </p>
  );
}
