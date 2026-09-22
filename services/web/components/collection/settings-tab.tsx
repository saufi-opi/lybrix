"use client";

/** KB Settings tab (WeKnora Tab 4): mandatory embedding-model rebind
 * (stale-dim warning), optional reranker binding, and the chunking-config
 * preview rendered from GET /v1/system/settings. */

import { useState } from "react";
import { toast } from "sonner";
import { adminFetch } from "@/components/ingest/ingest-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import type { CollectionStats } from "@/lib/api-client";
import { useEmbeddingModels, useRerankModels, useSystemSettings } from "@/lib/queries";

export function SettingsTab({
  collectionId,
  stats,
}: {
  collectionId: string;
  stats: CollectionStats | undefined;
}) {
  const { data: models } = useEmbeddingModels();
  const { data: rerankers } = useRerankModels();
  const settings = useSystemSettings();

  const modelList = (models ?? []) as unknown as Array<{
    id: string;
    name: string;
    model_id: string;
    vector_dim: number;
  }>;
  const rerankerList = (rerankers ?? []) as unknown as Array<{
    id: string;
    name: string;
    model_id: string;
  }>;

  const [rebindModel, setRebindModel] = useState("");
  const [rerankChoice, setRerankChoice] = useState("keep");
  const [busy, setBusy] = useState(false);

  async function rebind() {
    if (!rebindModel) return;
    setBusy(true);
    try {
      await adminFetch(`/api/admin/v1/collections/${collectionId}/model`, {
        method: "POST",
        body: JSON.stringify({ embedding_model_id: rebindModel }),
      });
      toast.success("model rebound — re-embed documents to refresh vectors", {
        description:
          "dense search excludes stale-dimension vectors until each document is re-embedded",
      });
      setRebindModel("");
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      toast.error("rebind failed", { description: msg });
    } finally {
      setBusy(false);
    }
  }

  async function bindReranker() {
    setBusy(true);
    try {
      const reranker = rerankChoice === "keep" || rerankChoice === "none" ? null : rerankChoice;
      await adminFetch(`/api/admin/v1/collections/${collectionId}/reranker`, {
        method: "POST",
        body: JSON.stringify({ rerank_model_id: reranker }),
      });
      toast.success(
        reranker
          ? "reranker bound — single-collection searches rerank by default"
          : "rerank stage disabled",
      );
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      toast.error("reranker bind failed", { description: msg });
    } finally {
      setBusy(false);
    }
  }

  const s = settings.data;

  return (
    <div className="flex flex-col gap-4">
      {/* ===== embedding model binding ===== */}
      <Card>
        <CardHeader>
          <CardTitle className="font-serif text-[17px] font-semibold">Embedding model</CardTitle>
          <CardDescription>
            {stats ? (
              <>
                bound: <code className="font-mono">{stats.embedding_model}</code> — every collection
                binds exactly one registry model; the binding is mandatory.
              </>
            ) : (
              "loading binding…"
            )}
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-wrap items-end gap-3">
          <div className="flex min-w-[260px] flex-1 flex-col gap-1.5">
            <Label_>New model</Label_>
            <Select value={rebindModel} onValueChange={setRebindModel}>
              <SelectTrigger aria-label="new embedding model" className="w-full">
                <SelectValue placeholder="pick a registered model" />
              </SelectTrigger>
              <SelectContent>
                {modelList.map((m) => (
                  <SelectItem key={m.id} value={m.id}>
                    {m.name} · {m.model_id} · {m.vector_dim}d
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {rebindModel && (
              <p className="m-0 text-[11.5px] text-warning">
                metadata sync only — dense search excludes stale-dimension vectors until each
                document is re-embedded.
              </p>
            )}
          </div>
          <Button type="button" onClick={rebind} disabled={busy || !rebindModel}>
            {busy ? "Rebinding…" : "Rebind"}
          </Button>
        </CardContent>
      </Card>

      {/* ===== reranker binding ===== */}
      <Card>
        <CardHeader>
          <CardTitle className="font-serif text-[17px] font-semibold">
            Reranker (optional)
          </CardTitle>
          <CardDescription>
            Cross-encoder re-scoring after hybrid retrieval; failure degrades to RRF order, never an
            error.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-wrap items-end gap-3">
          <div className="flex min-w-[260px] flex-1 flex-col gap-1.5">
            <Label_>Reranker</Label_>
            <Select value={rerankChoice} onValueChange={setRerankChoice}>
              <SelectTrigger aria-label="reranker" className="w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="keep">keep current binding</SelectItem>
                <SelectItem value="none">none — rerank stage off</SelectItem>
                {rerankerList.map((r) => (
                  <SelectItem key={r.id} value={r.id}>
                    {r.name} · {r.model_id}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <Button type="button" onClick={bindReranker} disabled={busy || rerankChoice === "keep"}>
            {busy ? "Applying…" : "Apply"}
          </Button>
        </CardContent>
      </Card>

      {/* ===== chunking config preview ===== */}
      <Card>
        <CardHeader>
          <CardTitle className="font-serif text-[17px] font-semibold">Chunking config</CardTitle>
          <CardDescription>
            Hierarchical parent-child windows from env-configured settings (read-only preview).
          </CardDescription>
        </CardHeader>
        <CardContent>
          {s ? (
            <div className="flex flex-col gap-2">
              <div className="flex flex-wrap items-center gap-2">
                <Badge className="rounded border-press bg-transparent font-mono text-[11.5px] text-press-deep">
                  parent {s.parent_tokens} (hard cap {s.parent_hard_cap})
                </Badge>
                <Badge className="rounded border-ledger bg-transparent font-mono text-[11.5px] text-ledger">
                  child {s.child_tokens}, stride {s.child_stride_tokens}
                </Badge>
                <Badge className="rounded border-sheet-edge bg-transparent font-mono text-[11.5px] text-muted-foreground">
                  shard {s.shard_pages}p
                </Badge>
              </div>
              <ul className="m-0 list-none space-y-1 font-mono text-[12px] text-muted-foreground">
                <li>
                  retry ladder — shard lease {s.shard_lease_seconds}s, max {s.max_shard_attempts}{" "}
                  attempts, embed max {s.embed_max_attempts}
                </li>
                <li>
                  backpressure — parse backlog cap {s.max_parse_backlog}, document page cap{" "}
                  {s.max_document_pages}
                </li>
                <li>
                  search — default top_k {s.search_default_top_k}, max {s.search_max_top_k}, rerank
                  pool {s.rerank_candidates}
                </li>
              </ul>
            </div>
          ) : settings.isError ? (
            <p className="text-redink">settings API unreachable: {settings.error?.message}</p>
          ) : (
            <div className="flex gap-2">
              <Skeleton className="h-6 w-40" />
              <Skeleton className="h-6 w-40" />
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

function Label_({ children }: { children: React.ReactNode }) {
  return <span className="text-xs font-semibold text-muted-foreground">{children}</span>;
}
