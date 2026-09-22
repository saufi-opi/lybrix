"use client";

/** Model registry UI (2.0.1). Mirrors keys-manager: all fetches go through
 * /api/admin/* so the browser never holds a bearer key; api_key is write-only
 * ("stored server-side, never displayed again"). Create/edit dialogs are
 * shadcn Dialogs; feedback via sonner. */

import { useCallback, useEffect, useState } from "react";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
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
import type { ModelOut } from "@/lib/client/types.gen";

async function adminFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: { "content-type": "application/json", ...(init?.headers ?? {}) },
  });
  if (!res.ok) {
    const detail = await res.text();
    throw new Error(`API ${res.status}: ${detail.slice(0, 300)}`);
  }
  if (res.status === 204) return undefined as T;
  return res.json() as Promise<T>;
}

const PROVIDERS = ["tei", "ollama", "openai"] as const;
type Provider = (typeof PROVIDERS)[number];

const DIM_PRESETS = [384, 768, 1024, 1536, 2000];

interface FormState {
  name: string;
  provider: Provider;
  model_id: string;
  ingest_url: string;
  query_url: string;
  api_key: string;
  vector_dim: string;
  query_prefix: string;
  batch_size: string;
  ctx_budget: string;
  truncate_chars: string;
  is_default: boolean;
}

const EMPTY_FORM: FormState = {
  name: "",
  provider: "tei",
  model_id: "",
  ingest_url: "",
  query_url: "",
  api_key: "",
  vector_dim: "1024",
  query_prefix: "search_query: ",
  batch_size: "48",
  ctx_budget: "1900",
  truncate_chars: "6000",
  is_default: false,
};

const PROVIDER_CLASS: Record<string, string> = {
  tei: "border-press bg-press-wash text-press-deep",
  ollama: "border-warning bg-ochre-wash text-warning",
  openai: "border-ledger bg-ledger-wash text-ledger",
};

export function ModelsManager() {
  const [models, setModels] = useState<ModelOut[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // create/edit modal state
  const [editing, setEditing] = useState<ModelOut | null>(null); // null = create
  const [formOpen, setFormOpen] = useState(false);
  const [form, setForm] = useState<FormState>(EMPTY_FORM);

  // delete confirm state
  const [deleting, setDeleting] = useState<ModelOut | null>(null);

  const load = useCallback(async () => {
    try {
      const rows = await adminFetch<ModelOut[]>("/api/admin/v1/models");
      setModels(rows);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      setModels([]);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  function openCreate() {
    setEditing(null);
    setForm(EMPTY_FORM);
    setFormOpen(true);
  }

  function openEdit(m: ModelOut) {
    setEditing(m);
    setForm({
      name: m.name,
      provider: m.provider as Provider,
      model_id: m.model_id,
      ingest_url: m.ingest_url,
      query_url: m.query_url,
      api_key: "", // never displayed again — blank means unchanged
      vector_dim: String(m.vector_dim),
      query_prefix: m.query_prefix ?? "search_query: ",
      batch_size: String(m.batch_size),
      ctx_budget: String(m.ctx_budget),
      truncate_chars: String(m.truncate_chars),
      is_default: m.is_default ?? false,
    });
    setFormOpen(true);
  }

  async function saveModel(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      const payload: Record<string, unknown> = {
        name: form.name.trim(),
        provider: form.provider,
        model_id: form.model_id.trim(),
        ingest_url: form.ingest_url.trim(),
        query_url: form.query_url.trim(),
        vector_dim: Number(form.vector_dim),
        query_prefix: form.query_prefix,
        batch_size: Number(form.batch_size),
        ctx_budget: Number(form.ctx_budget),
        truncate_chars: Number(form.truncate_chars),
        is_default: form.is_default,
      };
      if (form.api_key) payload.api_key = form.api_key;
      if (editing) {
        await adminFetch<ModelOut>(`/api/admin/v1/models/${editing.id}`, {
          method: "POST",
          body: JSON.stringify(payload),
        });
        toast.success(`model "${form.name}" updated`);
      } else {
        await adminFetch<ModelOut>("/api/admin/v1/models", {
          method: "POST",
          body: JSON.stringify(payload),
        });
        toast.success(`model "${form.name}" registered`);
      }
      setFormOpen(false);
      await load();
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      setError(msg);
      toast.error(editing ? "update failed" : "create failed", { description: msg });
    } finally {
      setBusy(false);
    }
  }

  async function testModel() {
    setBusy(true);
    try {
      const payload: Record<string, unknown> = {
        name: form.name.trim() || "probe",
        provider: form.provider,
        model_id: form.model_id.trim(),
        ingest_url: form.ingest_url.trim(),
        query_url: form.query_url.trim(),
        vector_dim: Number(form.vector_dim) || 0,
      };
      if (form.api_key) payload.api_key = form.api_key;
      const res = await adminFetch<{
        reachable: boolean;
        latency_ms: number | null;
        detail: string;
        vector_dim: number | null;
      }>("/api/admin/v1/models/test", { method: "POST", body: JSON.stringify(payload) });
      if (res.reachable) {
        toast.success(`probe ok (${res.latency_ms ?? "?"} ms)`, {
          description: res.vector_dim
            ? `detected dim ${res.vector_dim}${res.vector_dim !== Number(form.vector_dim) ? " — declared dim differs" : ""}`
            : res.detail,
        });
      } else {
        toast.error("probe failed", { description: res.detail });
      }
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      toast.error("probe failed", { description: msg });
    } finally {
      setBusy(false);
    }
  }

  async function deleteModel() {
    if (!deleting) return;
    setBusy(true);
    try {
      await adminFetch(`/api/admin/v1/models/${deleting.id}`, { method: "DELETE" });
      toast.success(`model "${deleting.name}" deleted`);
      setDeleting(null);
      await load();
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      setError(msg);
      toast.error("delete failed", { description: msg });
    } finally {
      setBusy(false);
    }
  }

  async function setDefault(m: ModelOut) {
    setBusy(true);
    try {
      await adminFetch<ModelOut>(`/api/admin/v1/models/${m.id}`, {
        method: "POST",
        body: JSON.stringify({ is_default: true }),
      });
      toast.success(`"${m.name}" is now the default model`);
      await load();
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      toast.error("set default failed", { description: msg });
    } finally {
      setBusy(false);
    }
  }

  if (models === null) return <p className="text-muted-foreground">Loading models…</p>;

  return (
    <>
      <Card>
        <CardHeader>
          <div className="flex items-center justify-between gap-3">
            <CardTitle className="font-serif text-[19px] font-semibold">Embedding models</CardTitle>
            <Button type="button" onClick={openCreate}>
              + Register model
            </Button>
          </div>
          <CardDescription>
            Every collection binds one of these models at creation. Dimensions are arbitrary
            (1–2000); a partial HNSW index is provisioned per dimension on first use.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {error && (
            <p className="mt-0 mb-3 font-mono text-[12.5px] text-redink" role="alert">
              {error}
            </p>
          )}
          {models.length === 0 ? (
            <div className="py-10 text-center">
              <div className="mb-2 text-[28px] text-muted-foreground">◇</div>
              <p className="m-0 mb-1 font-semibold">No models registered</p>
              <p className="m-0 text-muted-foreground">
                Register a TEI, Ollama, or OpenAI-compatible endpoint to start ingesting.
              </p>
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                    name
                  </TableHead>
                  <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                    provider
                  </TableHead>
                  <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                    model
                  </TableHead>
                  <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                    dim
                  </TableHead>
                  <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                    ingest / query
                  </TableHead>
                  <TableHead className="text-right text-[11.5px] tracking-[0.02em] text-muted-foreground">
                    actions
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {models.map((m) => (
                  <TableRow key={m.id}>
                    <TableCell>
                      <span className="font-medium">{m.name}</span>
                      {m.is_default && (
                        <Badge className="ml-2 rounded bg-press-wash font-mono text-[0.65rem] text-press-deep">
                          default
                        </Badge>
                      )}
                      {m.has_api_key && (
                        <Badge className="ml-1.5 rounded border-dashed bg-transparent font-mono text-[0.65rem] text-muted-foreground">
                          key
                        </Badge>
                      )}
                    </TableCell>
                    <TableCell>
                      <span
                        className={
                          "inline-block rounded-[2px] border px-2 py-0.5 font-mono text-[0.72rem] font-semibold " +
                          (PROVIDER_CLASS[m.provider] ?? "")
                        }
                      >
                        {m.provider}
                      </span>
                    </TableCell>
                    <TableCell className="font-mono text-[12.5px]">{m.model_id}</TableCell>
                    <TableCell className="tabular-nums">{m.vector_dim}</TableCell>
                    <TableCell className="max-w-[220px] truncate font-mono text-[11.5px] text-muted-foreground">
                      {m.ingest_url} / {m.query_url}
                    </TableCell>
                    <TableCell className="text-right">
                      <div className="inline-flex items-center gap-1.5">
                        {!m.is_default && (
                          <Button
                            type="button"
                            variant="outline"
                            size="xs"
                            disabled={busy}
                            onClick={() => setDefault(m)}
                          >
                            Set default
                          </Button>
                        )}
                        <Button
                          type="button"
                          variant="outline"
                          size="xs"
                          disabled={busy}
                          onClick={() => openEdit(m)}
                        >
                          Edit
                        </Button>
                        <Button
                          type="button"
                          variant="destructive"
                          size="xs"
                          disabled={busy}
                          onClick={() => setDeleting(m)}
                        >
                          Delete
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {/* ===== create / edit modal ===== */}
      <Dialog open={formOpen} onOpenChange={(open) => !busy && setFormOpen(open)}>
        <DialogContent className="max-h-[85vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle className="font-serif text-[17px] font-semibold">
              {editing ? "Edit model" : "Register model"}
            </DialogTitle>
            <DialogDescription>
              {editing
                ? "api_key is stored server-side — leave it blank to keep the existing key."
                : "The dim is confirmed against the live endpoint at save time."}
            </DialogDescription>
          </DialogHeader>
          <form onSubmit={saveModel} className="flex flex-col gap-3.5">
            <div className="grid grid-cols-1 gap-3.5 min-[640px]:grid-cols-2">
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="m-name" className="text-xs font-semibold text-muted-foreground">
                  Name
                </Label>
                <Input
                  id="m-name"
                  placeholder="bge-m3 @ ingest host"
                  value={form.name}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                  required
                />
              </div>
              <div className="flex flex-col gap-1.5">
                <Label className="text-xs font-semibold text-muted-foreground">Provider</Label>
                <Select
                  value={form.provider}
                  onValueChange={(v) => setForm({ ...form, provider: v as Provider })}
                >
                  <SelectTrigger aria-label="provider" className="w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {PROVIDERS.map((p) => (
                      <SelectItem key={p} value={p}>
                        {p}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="m-model" className="text-xs font-semibold text-muted-foreground">
                  Model id
                </Label>
                <Input
                  id="m-model"
                  placeholder="BAAI/bge-m3"
                  value={form.model_id}
                  onChange={(e) => setForm({ ...form, model_id: e.target.value })}
                  required
                />
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="m-dim" className="text-xs font-semibold text-muted-foreground">
                  Vector dimension (1–2000)
                </Label>
                <Input
                  id="m-dim"
                  type="number"
                  min={1}
                  max={2000}
                  value={form.vector_dim}
                  onChange={(e) => setForm({ ...form, vector_dim: e.target.value })}
                  required
                />
                <div className="flex flex-wrap gap-1.5">
                  {DIM_PRESETS.map((d) => (
                    <button
                      key={d}
                      type="button"
                      className={
                        "min-h-[28px] rounded-[2px] border px-2 font-mono text-[11.5px] transition-colors duration-300 " +
                        (form.vector_dim === String(d)
                          ? "border-press bg-press-wash text-press-deep"
                          : "border-sheet-edge text-muted-foreground hover:border-ink-faint")
                      }
                      onClick={() => setForm({ ...form, vector_dim: String(d) })}
                    >
                      {d}
                    </button>
                  ))}
                </div>
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="m-ingest" className="text-xs font-semibold text-muted-foreground">
                  Ingest URL
                </Label>
                <Input
                  id="m-ingest"
                  placeholder="http://tei-ingest:80"
                  value={form.ingest_url}
                  onChange={(e) => setForm({ ...form, ingest_url: e.target.value })}
                  required
                />
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="m-query" className="text-xs font-semibold text-muted-foreground">
                  Query URL
                </Label>
                <Input
                  id="m-query"
                  placeholder="http://tei-query:80"
                  value={form.query_url}
                  onChange={(e) => setForm({ ...form, query_url: e.target.value })}
                  required
                />
              </div>
              {(form.provider === "openai" || editing?.has_api_key) && (
                <div className="flex flex-col gap-1.5 min-[640px]:col-span-2">
                  <Label htmlFor="m-key" className="text-xs font-semibold text-muted-foreground">
                    API key {editing?.has_api_key && "(set — leave blank to keep)"}
                  </Label>
                  <Input
                    id="m-key"
                    type="password"
                    placeholder="stored server-side, never displayed again"
                    value={form.api_key}
                    onChange={(e) => setForm({ ...form, api_key: e.target.value })}
                  />
                </div>
              )}
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="m-prefix" className="text-xs font-semibold text-muted-foreground">
                  Query prefix
                </Label>
                <Input
                  id="m-prefix"
                  value={form.query_prefix}
                  onChange={(e) => setForm({ ...form, query_prefix: e.target.value })}
                />
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="m-batch" className="text-xs font-semibold text-muted-foreground">
                  Batch size
                </Label>
                <Input
                  id="m-batch"
                  type="number"
                  min={1}
                  value={form.batch_size}
                  onChange={(e) => setForm({ ...form, batch_size: e.target.value })}
                />
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="m-budget" className="text-xs font-semibold text-muted-foreground">
                  Ctx budget (tokens)
                </Label>
                <Input
                  id="m-budget"
                  type="number"
                  min={1}
                  value={form.ctx_budget}
                  onChange={(e) => setForm({ ...form, ctx_budget: e.target.value })}
                />
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="m-trunc" className="text-xs font-semibold text-muted-foreground">
                  Truncate chars
                </Label>
                <Input
                  id="m-trunc"
                  type="number"
                  min={0}
                  value={form.truncate_chars}
                  onChange={(e) => setForm({ ...form, truncate_chars: e.target.value })}
                />
              </div>
            </div>
            <Label className="cursor-pointer gap-2 text-[13px]">
              <input
                type="checkbox"
                checked={form.is_default}
                onChange={(e) => setForm({ ...form, is_default: e.target.checked })}
                className="size-[15px] cursor-pointer accent-[var(--press)]"
              />
              Default model (unbound collections resolve here)
            </Label>
            <DialogFooter>
              <Button type="button" variant="outline" onClick={testModel} disabled={busy}>
                Test
              </Button>
              <Button
                type="button"
                variant="outline"
                onClick={() => setFormOpen(false)}
                disabled={busy}
              >
                Cancel
              </Button>
              <Button type="submit" disabled={busy}>
                {busy ? "Saving…" : editing ? "Save changes" : "Register"}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      {/* ===== delete modal ===== */}
      <Dialog open={deleting !== null} onOpenChange={(open) => !open && !busy && setDeleting(null)}>
        <DialogContent showCloseButton={false}>
          <DialogHeader>
            <DialogTitle className="font-serif text-[17px] font-semibold">Delete model</DialogTitle>
            <DialogDescription>
              Delete <strong className="text-ink">{deleting?.name}</strong>? Collections bound to it
              (or the default) block deletion with 409. This cannot be undone.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => setDeleting(null)}
              disabled={busy}
            >
              Cancel
            </Button>
            <Button type="button" variant="destructive" onClick={deleteModel} disabled={busy}>
              {busy ? "Deleting…" : "Delete model"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  );
}
