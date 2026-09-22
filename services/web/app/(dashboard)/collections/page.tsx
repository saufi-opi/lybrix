"use client";

/** Collections (2.0.1): each collection binds one registry model at
 * creation (mandatory) and inherits its model_id + vector_dim; rebinding
 * is available per collection via the bind endpoint. Client component —
 * useCollections() refetches on window focus. */

import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useCollections, useEmbeddingModels } from "@/lib/queries";

interface CollectionRow {
  id: string;
  name: string;
  embedding_model: string;
  vector_dim?: number;
  embedding_model_id?: string | null;
  doc_count?: number;
}

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

export default function CollectionsPage() {
  const { data: cols, isPending, isError, error, refetch } = useCollections();
  const { data: models } = useEmbeddingModels();

  const [creating, setCreating] = useState(false);
  const [id, setId] = useState("");
  const [name, setName] = useState("");
  const [modelId, setModelId] = useState("");
  const [busy, setBusy] = useState(false);

  // rebind state: { collectionId, currentModelId }
  const [rebinding, setRebinding] = useState<CollectionRow | null>(null);
  const [rebindModel, setRebindModel] = useState("");

  const modelList = (models ?? []) as unknown as Array<{
    id: string;
    name: string;
    model_id: string;
    vector_dim: number;
  }>;

  const loadCols = useCallback(async () => {
    await refetch();
  }, [refetch]);

  useEffect(() => {
    // keep doc_count fresh after mutations land via refetch on focus
  }, []);

  async function createCollection(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      await adminFetch("/api/admin/v1/collections", {
        method: "POST",
        body: JSON.stringify({ id: id.trim(), name: name.trim(), embedding_model_id: modelId }),
      });
      toast.success(`collection "${id.trim()}" created`);
      setCreating(false);
      setId("");
      setName("");
      setModelId("");
      await loadCols();
    } catch (e2) {
      const msg = e2 instanceof Error ? e2.message : String(e2);
      toast.error("create failed", { description: msg });
    } finally {
      setBusy(false);
    }
  }

  async function rebind() {
    if (!rebinding) return;
    setBusy(true);
    try {
      await adminFetch(`/api/admin/v1/collections/${rebinding.id}/model`, {
        method: "POST",
        body: JSON.stringify({ embedding_model_id: rebindModel }),
      });
      toast.success(`"${rebinding.id}" rebound — run per-doc re-embeds to refresh vectors`);
      setRebinding(null);
      await loadCols();
    } catch (e2) {
      const msg = e2 instanceof Error ? e2.message : String(e2);
      toast.error("rebind failed", { description: msg });
    } finally {
      setBusy(false);
    }
  }

  const selectedModel = modelList.find((m) => m.id === modelId);

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between gap-3">
          <CardTitle className="font-serif text-[19px] font-semibold">Collections</CardTitle>
          <Button type="button" onClick={() => setCreating(true)}>
            + New collection
          </Button>
        </div>
        <CardDescription>
          Each collection binds a registry model at creation and inherits its dimension — manage
          models on the Models page; rebinding is available per collection.
        </CardDescription>
      </CardHeader>
      <CardContent>
        {isPending ? (
          <p className="m-0 text-muted-foreground">loading collections…</p>
        ) : isError ? (
          <p className="m-0 text-redink">collections API unreachable: {error.message}</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  id
                </TableHead>
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  name
                </TableHead>
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  model
                </TableHead>
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  dim
                </TableHead>
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  docs
                </TableHead>
                <TableHead className="text-right text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  actions
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(cols as unknown as CollectionRow[]).map((c) => (
                <TableRow key={c.id}>
                  <TableCell className="font-mono text-[12.5px]">{c.id}</TableCell>
                  <TableCell className="font-medium">{c.name}</TableCell>
                  <TableCell className="font-mono text-[12.5px]">{c.embedding_model}</TableCell>
                  <TableCell className="tabular-nums">{c.vector_dim ?? "—"}</TableCell>
                  <TableCell className="tabular-nums">{c.doc_count ?? "—"}</TableCell>
                  <TableCell className="text-right">
                    <Button
                      type="button"
                      variant="outline"
                      size="xs"
                      onClick={() => {
                        setRebinding(c);
                        setRebindModel(c.embedding_model_id ?? "");
                      }}
                    >
                      Rebind model
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>

      {/* ===== create modal ===== */}
      <Dialog open={creating} onOpenChange={(open) => !busy && setCreating(open)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle className="font-serif text-[17px] font-semibold">
              New collection
            </DialogTitle>
          </DialogHeader>
          <form onSubmit={createCollection} className="flex flex-col gap-3.5">
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="col-id" className="text-xs font-semibold text-muted-foreground">
                Id
              </Label>
              <Input
                id="col-id"
                placeholder="e.g. calibre"
                value={id}
                onChange={(e) => setId(e.target.value)}
                maxLength={64}
                required
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="col-name" className="text-xs font-semibold text-muted-foreground">
                Name
              </Label>
              <Input
                id="col-name"
                placeholder="display name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                required
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label className="text-xs font-semibold text-muted-foreground">
                Embedding model (required)
              </Label>
              <Select value={modelId} onValueChange={setModelId}>
                <SelectTrigger aria-label="embedding model" className="w-full">
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
              {selectedModel && (
                <p className="m-0 text-[11.5px] text-muted-foreground">
                  inherits dimension {selectedModel.vector_dim}
                </p>
              )}
            </div>
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                onClick={() => setCreating(false)}
                disabled={busy}
              >
                Cancel
              </Button>
              <Button type="submit" disabled={busy || !modelId}>
                {busy ? "Creating…" : "Create collection"}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      {/* ===== rebind modal ===== */}
      <Dialog
        open={rebinding !== null}
        onOpenChange={(open) => !open && !busy && setRebinding(null)}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle className="font-serif text-[17px] font-semibold">
              Rebind model — {rebinding?.id}
            </DialogTitle>
          </DialogHeader>
          <p className="m-0 text-[13px] text-muted-foreground">
            Metadata sync only: dense search excludes stale-dimension vectors until each document is
            re-embedded (per-doc re-embed endpoints).
          </p>
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs font-semibold text-muted-foreground">New model</Label>
            <Select value={rebindModel} onValueChange={setRebindModel}>
              <SelectTrigger aria-label="new model" className="w-full">
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
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => setRebinding(null)}
              disabled={busy}
            >
              Cancel
            </Button>
            <Button type="button" onClick={rebind} disabled={busy || !rebindModel}>
              {busy ? "Rebinding…" : "Rebind"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  );
}
