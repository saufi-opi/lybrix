"use client";

/** Knowledge Bases (WeKnora gallery): grid/list toggle over per-card
 * doc/chunk counts + bound model + quick actions (open, ingest). Stats ride
 * /v1/collections/{id}/stats per card (TanStack useQueries fan). Create /
 * rebind / reranker dialogs are preserved from 2.0.1. */

import { useQueries } from "@tanstack/react-query";
import { cn } from "cn";
import {
  Database,
  FileText,
  HardDrive,
  Layers,
  LayoutGrid,
  List,
  Plus,
  Upload,
} from "lucide-react";
import Link from "next/link";
import { useCallback, useMemo, useState } from "react";
import { toast } from "sonner";
import { GsapStagger } from "@/components/gsap-stagger";
import { adminFetch, IngestDialog } from "@/components/ingest/ingest-dialog";
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
import type { CollectionStats } from "@/lib/api-client";
import { useCollections, useEmbeddingModels, useRerankModels } from "@/lib/queries";

interface CollectionRow {
  id: string;
  name: string;
  embedding_model: string;
  vector_dim?: number;
  embedding_model_id?: string | null;
  rerank_model_id?: string | null;
}

function formatBytes(n: number): string {
  if (n >= 1024 * 1024 * 1024) return `${(n / (1024 * 1024 * 1024)).toFixed(1)} GB`;
  if (n >= 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MB`;
  if (n >= 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${n} B`;
}

/** fan one stats query per KB card — the gallery needs per-card numbers and
 * useQueries collapses the loading state across the whole fan. */
function useStatsFan(ids: string[]) {
  return useQueries({
    queries: ids.map((id) => ({
      queryKey: ["collection-stats", id],
      queryFn: () => adminFetch<CollectionStats>(`/api/admin/v1/collections/${id}/stats`),
      // shared cache key with useCollectionStats — no duplicate fetches
      staleTime: 5_000,
    })),
  });
}

export default function CollectionsPage() {
  const { data: cols, isPending, isError, error, refetch } = useCollections();
  const { data: models } = useEmbeddingModels();
  const { data: rerankers } = useRerankModels();

  const [view, setView] = useState<"grid" | "list">("grid");
  const [ingestFor, setIngestFor] = useState<string | null>(null);

  const collectionList = (cols ?? []) as unknown as CollectionRow[];
  const statsFan = useStatsFan(useMemo(() => collectionList.map((c) => c.id), [collectionList]));
  const statsById = useMemo(() => {
    const m = new Map<string, CollectionStats | undefined>();
    for (let i = 0; i < collectionList.length; i++) {
      m.set(collectionList[i].id, statsFan[i]?.data);
    }
    return m;
  }, [collectionList, statsFan]);

  const [creating, setCreating] = useState(false);
  const [id, setId] = useState("");
  const [name, setName] = useState("");
  const [modelId, setModelId] = useState("");
  const [busy, setBusy] = useState(false);

  // rebind state: { collectionId, currentModelId }
  const [rebinding, setRebinding] = useState<CollectionRow | null>(null);
  const [rebindModel, setRebindModel] = useState("");

  // reranker binding state
  const [reranking, setReranking] = useState<CollectionRow | null>(null);
  const [rerankChoice, setRerankChoice] = useState("none");

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

  const loadCols = useCallback(async () => {
    await refetch();
  }, [refetch]);

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

  async function bindReranker() {
    if (!reranking) return;
    setBusy(true);
    try {
      const reranker = rerankChoice === "none" ? null : rerankChoice;
      await adminFetch(`/api/admin/v1/collections/${reranking.id}/reranker`, {
        method: "POST",
        body: JSON.stringify({ rerank_model_id: reranker }),
      });
      toast.success(
        reranker
          ? `"${reranking.id}" bound to the reranker — single-collection searches rerank by default`
          : `"${reranking.id}" rerank stage disabled`,
      );
      setReranking(null);
      await loadCols();
    } catch (e2) {
      const msg = e2 instanceof Error ? e2.message : String(e2);
      toast.error("reranker bind failed", { description: msg });
    } finally {
      setBusy(false);
    }
  }

  const selectedModel = modelList.find((m) => m.id === modelId);

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between gap-3">
          <CardTitle className="font-serif text-[19px] font-semibold">Knowledge Bases</CardTitle>
          <div className="flex items-center gap-2">
            <div className="inline-flex overflow-hidden rounded border border-sheet-edge">
              <button
                type="button"
                aria-label="grid view"
                aria-pressed={view === "grid"}
                className={cn(
                  "flex size-8 items-center justify-center transition-colors duration-300",
                  view === "grid"
                    ? "bg-press-wash text-press-deep"
                    : "text-muted-foreground hover:bg-white/5",
                )}
                onClick={() => setView("grid")}
              >
                <LayoutGrid className="size-4" aria-hidden="true" />
              </button>
              <button
                type="button"
                aria-label="list view"
                aria-pressed={view === "list"}
                className={cn(
                  "flex size-8 items-center justify-center transition-colors duration-300",
                  view === "list"
                    ? "bg-press-wash text-press-deep"
                    : "text-muted-foreground hover:bg-white/5",
                )}
                onClick={() => setView("list")}
              >
                <List className="size-4" aria-hidden="true" />
              </button>
            </div>
            <Button type="button" onClick={() => setCreating(true)}>
              <Plus aria-hidden="true" />
              New collection
            </Button>
          </div>
        </div>
        <CardDescription>
          Each knowledge base binds a registry model at creation and inherits its dimension — open a
          KB to ingest, inspect chunks, and test retrieval; manage models on the Models page.
        </CardDescription>
      </CardHeader>
      <CardContent>
        {isPending ? (
          <p className="m-0 text-muted-foreground">loading collections…</p>
        ) : isError ? (
          <p className="m-0 text-redink">collections API unreachable: {error.message}</p>
        ) : collectionList.length === 0 ? (
          <div className="py-10 text-center">
            <div className="mb-2 text-[28px] text-muted-foreground">◇</div>
            <p className="m-0 mb-1 font-semibold">No knowledge bases yet</p>
            <p className="m-0 text-muted-foreground">
              Create a collection bound to a registered embedding model to start ingesting.
            </p>
          </div>
        ) : view === "grid" ? (
          <GsapStagger>
            <div className="grid grid-cols-1 gap-3 min-[640px]:grid-cols-2 min-[1024px]:grid-cols-3">
              {collectionList.map((c, i) => {
                const st = statsById.get(c.id);
                return (
                  <Link
                    key={c.id}
                    href={`/collections/${c.id}`}
                    className="hover-tactile kb-card-reveal block rounded-lg border border-sheet-edge bg-sheet p-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
                    data-stagger={i}
                  >
                    <div className="mb-2 flex items-center gap-2">
                      <Database className="size-4 shrink-0 text-press" aria-hidden="true" />
                      <span className="min-w-0 flex-1 truncate font-semibold">{c.name}</span>
                      <span className="font-mono text-[10.5px] text-muted-foreground">{c.id}</span>
                    </div>
                    <div className="flex flex-wrap gap-x-4 gap-y-1 font-mono text-[11.5px] text-muted-foreground">
                      <span className="inline-flex items-center gap-1.5">
                        <FileText className="size-3.5" aria-hidden="true" />
                        {st ? st.doc_count : "…"}
                      </span>
                      <span className="inline-flex items-center gap-1.5">
                        <Layers className="size-3.5" aria-hidden="true" />
                        {st ? st.chunk_count : "…"}
                      </span>
                      <span className="inline-flex items-center gap-1.5">
                        <HardDrive className="size-3.5" aria-hidden="true" />
                        {st ? formatBytes(st.byte_size) : "…"}
                      </span>
                    </div>
                    <p className="m-0 mt-2 truncate font-mono text-[11px] text-muted-foreground">
                      {c.embedding_model}
                      {c.vector_dim ? ` · ${c.vector_dim}d` : ""}
                    </p>
                  </Link>
                );
              })}
            </div>
          </GsapStagger>
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
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  chunks
                </TableHead>
                <TableHead className="text-right text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  actions
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {collectionList.map((c) => {
                const st = statsById.get(c.id);
                return (
                  <TableRow key={c.id}>
                    <TableCell>
                      <Link
                        href={`/collections/${c.id}`}
                        className="font-mono text-[12.5px] hover:text-press"
                      >
                        {c.id}
                      </Link>
                    </TableCell>
                    <TableCell className="font-medium">{c.name}</TableCell>
                    <TableCell className="font-mono text-[12.5px]">{c.embedding_model}</TableCell>
                    <TableCell className="tabular-nums">{c.vector_dim ?? "—"}</TableCell>
                    <TableCell className="tabular-nums">{st ? st.doc_count : "—"}</TableCell>
                    <TableCell className="tabular-nums">{st ? st.chunk_count : "—"}</TableCell>
                    <TableCell className="text-right">
                      <div className="inline-flex items-center gap-1.5">
                        <Button
                          type="button"
                          variant="outline"
                          size="xs"
                          onClick={() => setIngestFor(c.id)}
                        >
                          <Upload className="size-3.5" aria-hidden="true" />
                          Ingest
                        </Button>
                        <Button
                          type="button"
                          variant="outline"
                          size="xs"
                          onClick={() => {
                            setReranking(c);
                            setRerankChoice(c.rerank_model_id ?? "none");
                          }}
                        >
                          Reranker
                        </Button>
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
                      </div>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
      </CardContent>

      {/* ===== quick ingest dialog (shared hub, pre-bound) ===== */}
      {ingestFor && (
        <IngestDialog
          open={ingestFor !== null}
          onOpenChange={(open) => !open && setIngestFor(null)}
          collectionId={ingestFor}
          onDone={loadCols}
        />
      )}

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

      {/* ===== reranker bind modal ===== */}
      <Dialog
        open={reranking !== null}
        onOpenChange={(open) => !open && !busy && setReranking(null)}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle className="font-serif text-[17px] font-semibold">
              Rerank stage — {reranking?.id}
            </DialogTitle>
          </DialogHeader>
          <p className="m-0 text-[13px] text-muted-foreground">
            A bound reranker runs on single-collection searches with that collection as the target
            (hybrid retrieval first, then cross-encoder re-scoring). &ldquo;None&rdquo; disables the
            stage; rerank failure degrades to RRF order, never an error. Multi-collection searches
            require an explicit reranker per request.
          </p>
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs font-semibold text-muted-foreground">Reranker</Label>
            <Select value={rerankChoice} onValueChange={setRerankChoice}>
              <SelectTrigger aria-label="reranker" className="w-full">
                <SelectValue placeholder="none (rerank off)" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="none">None — rerank stage off</SelectItem>
                {rerankerList.map((r) => (
                  <SelectItem key={r.id} value={r.id}>
                    {r.name} · {r.model_id}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => setReranking(null)}
              disabled={busy}
            >
              Cancel
            </Button>
            <Button type="button" onClick={bindReranker} disabled={busy}>
              {busy ? "Applying…" : "Apply"}
            </Button>
          </DialogFooter>
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
