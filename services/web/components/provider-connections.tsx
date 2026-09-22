"use client";

/** Provider Connections — Models page third tab (WeKnora structure): a
 * per-registry-row "Test Connection" probe reusing the POST /v1/models/test
 * and POST /v1/rerank-models/test dry-run endpoints, with protocol badges
 * from the provider field. No mutation — the probes are dry runs. */

import { PlugZap } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import type { ModelOut, RerankModelOut } from "@/lib/client/types.gen";

async function adminFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: { "content-type": "application/json", ...(init?.headers ?? {}) },
  });
  if (!res.ok) {
    const detail = await res.text();
    throw new Error(`API ${res.status}: ${detail.slice(0, 300)}`);
  }
  return res.json() as Promise<T>;
}

const PROVIDER_CLASS: Record<string, string> = {
  tei: "border-press bg-press-wash text-press-deep",
  ollama: "border-warning bg-ochre-wash text-warning",
  openai: "border-ledger bg-ledger-wash text-ledger",
};

function ProviderBadge({ provider }: { provider: string }) {
  return (
    <span
      className={
        "inline-block rounded-[2px] border px-2 py-0.5 font-mono text-[0.72rem] font-semibold " +
        (PROVIDER_CLASS[provider] ?? "")
      }
    >
      {provider}
    </span>
  );
}

type ProbeState = Record<string, "idle" | "running" | "ok" | "failed">;

export function ProviderConnections() {
  const [models, setModels] = useState<ModelOut[]>([]);
  const [rerankers, setRerankers] = useState<RerankModelOut[]>([]);
  const [probe, setProbe] = useState<ProbeState>({});
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const [m, r] = await Promise.all([
        adminFetch<ModelOut[]>("/api/admin/v1/models"),
        adminFetch<RerankModelOut[]>("/api/admin/v1/rerank-models"),
      ]);
      setModels(m);
      setRerankers(r);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const setState = (id: string, s: ProbeState[string]) =>
    setProbe((prev) => ({ ...prev, [id]: s }));

  async function testModel(m: ModelOut) {
    setState(m.id, "running");
    try {
      const res = await adminFetch<{
        reachable: boolean;
        latency_ms: number | null;
        detail: string;
        vector_dim: number | null;
      }>("/api/admin/v1/models/test", {
        method: "POST",
        body: JSON.stringify({
          name: m.name,
          provider: m.provider,
          model_id: m.model_id,
          ingest_url: m.ingest_url,
          query_url: m.query_url,
          vector_dim: m.vector_dim,
        }),
      });
      if (res.reachable) {
        setState(m.id, "ok");
        toast.success(`${m.name}: probe ok (${res.latency_ms ?? "?"} ms)`, {
          description: res.vector_dim ? `detected dim ${res.vector_dim}` : res.detail,
        });
      } else {
        setState(m.id, "failed");
        toast.error(`${m.name}: probe failed`, { description: res.detail });
      }
    } catch (e) {
      setState(m.id, "failed");
      toast.error(`${m.name}: probe failed`, {
        description: e instanceof Error ? e.message : String(e),
      });
    }
  }

  async function testReranker(r: RerankModelOut) {
    setState(r.id, "running");
    try {
      const res = await adminFetch<{
        reachable: boolean;
        latency_ms: number | null;
        detail: string;
      }>("/api/admin/v1/rerank-models/test", {
        method: "POST",
        body: JSON.stringify({
          name: r.name,
          provider: r.provider,
          model_id: r.model_id,
          query_url: r.query_url,
          truncate_chars: r.truncate_chars,
        }),
      });
      if (res.reachable) {
        setState(r.id, "ok");
        toast.success(`${r.name}: probe ok (${res.latency_ms ?? "?"} ms)`);
      } else {
        setState(r.id, "failed");
        toast.error(`${r.name}: probe failed`, { description: res.detail });
      }
    } catch (e) {
      setState(r.id, "failed");
      toast.error(`${r.name}: probe failed`, {
        description: e instanceof Error ? e.message : String(e),
      });
    }
  }

  const probeLabel = (id: string) => {
    const s = probe[id] ?? "idle";
    return s === "running"
      ? "Probing…"
      : s === "ok"
        ? "Reachable"
        : s === "failed"
          ? "Down"
          : "Test Connection";
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-serif text-[19px] font-semibold">Provider connections</CardTitle>
        <CardDescription>
          Dry-run probes against every registered endpoint (no mutation) — embedding and reranker
          endpoints are probed with the same requests the registry uses at save time.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {error && (
          <p className="mt-0 mb-0 font-mono text-[12.5px] text-redink" role="alert">
            {error}
          </p>
        )}
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                name
              </TableHead>
              <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                protocol
              </TableHead>
              <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                kind
              </TableHead>
              <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                endpoint
              </TableHead>
              <TableHead className="text-right text-[11.5px] tracking-[0.02em] text-muted-foreground">
                probe
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {models.map((m) => (
              <TableRow key={`m-${m.id}`}>
                <TableCell className="font-medium">{m.name}</TableCell>
                <TableCell>
                  <ProviderBadge provider={m.provider} />
                </TableCell>
                <TableCell className="font-mono text-[11.5px] text-muted-foreground">
                  embedding
                </TableCell>
                <TableCell className="max-w-[220px] truncate font-mono text-[11.5px] text-muted-foreground">
                  {m.query_url}
                </TableCell>
                <TableCell className="text-right">
                  <Button
                    type="button"
                    variant="outline"
                    size="xs"
                    disabled={probe[m.id] === "running"}
                    onClick={() => void testModel(m)}
                  >
                    <PlugZap className="size-3.5" aria-hidden="true" />
                    {probeLabel(m.id)}
                  </Button>
                </TableCell>
              </TableRow>
            ))}
            {rerankers.map((r) => (
              <TableRow key={`r-${r.id}`}>
                <TableCell className="font-medium">{r.name}</TableCell>
                <TableCell>
                  <ProviderBadge provider={r.provider} />
                </TableCell>
                <TableCell className="font-mono text-[11.5px] text-muted-foreground">
                  reranker
                </TableCell>
                <TableCell className="max-w-[220px] truncate font-mono text-[11.5px] text-muted-foreground">
                  {r.query_url}
                </TableCell>
                <TableCell className="text-right">
                  <Button
                    type="button"
                    variant="outline"
                    size="xs"
                    disabled={probe[r.id] === "running"}
                    onClick={() => void testReranker(r)}
                  >
                    <PlugZap className="size-3.5" aria-hidden="true" />
                    {probeLabel(r.id)}
                  </Button>
                </TableCell>
              </TableRow>
            ))}
            {models.length === 0 && rerankers.length === 0 && (
              <TableRow>
                <TableCell colSpan={5} className="py-8 text-center text-muted-foreground">
                  nothing registered yet — register a model on the Embedding models tab
                </TableCell>
              </TableRow>
            )}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  );
}
