"use client";

/** The three retry buttons (PRD §8.3): they cost very different amounts
 * — retry failed shards = seconds, re-embed = ~30s, reprocess = minutes.
 * Never offer only "retry". */

import { useState } from "react";

export function RetryButtons({ docId }: { docId: string }) {
  const [busy, setBusy] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);

  async function run(scope: string, label: string) {
    setBusy(scope);
    setMsg(null);
    try {
      const res = await fetch(`/api/admin/v1/documents/${docId}/retry`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ scope }),
      });
      if (!res.ok) throw new Error(`API ${res.status}: ${(await res.text()).slice(0, 200)}`);
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
