"use client";

/** KB Documents tab (WeKnora Tab 1): batch action bar (select-all, batch
 * delete / batch re-parse over POST /v1/documents/batch), search + status +
 * format filters, status pills, per-row actions (open, retry, delete). */

import { RefreshCw, Search, Trash2 } from "lucide-react";
import { useRouter } from "next/navigation";
import { useMemo, useState } from "react";
import { toast } from "sonner";
import { adminFetch } from "@/components/ingest/ingest-dialog";
import { StateBadge } from "@/components/state-badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
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
import type { DocumentRow } from "@/lib/api-client";
import { useDocuments } from "@/lib/queries";

/** DocState values the status filter maps onto (all/parsing/ready/failed —
 * "parsing" covers the whole non-terminal pipeline). */
const STATUS_FILTERS = ["all", "parsing", "ready", "failed"] as const;
type StatusFilter = (typeof STATUS_FILTERS)[number];

function formatBytes(n: number | null): string {
  if (n == null) return "—";
  if (n >= 1024 * 1024 * 1024) return `${(n / (1024 * 1024 * 1024)).toFixed(1)} GB`;
  if (n >= 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MB`;
  if (n >= 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${n} B`;
}

/** DocumentOut carries no format field — derive from the title suffix. */
function titleFormat(title: string | null): string {
  if (!title) return "—";
  const m = /\.([a-z0-9]+)$/i.exec(title.trim());
  return m ? m[1].toUpperCase() : "—";
}

const PARSING_STATES = new Set(["uploaded", "splitting", "parsing", "embedding", "indexing"]);

export function DocumentsTab({ collectionId }: { collectionId: string }) {
  const router = useRouter();
  const [q, setQ] = useState("");
  const [status, setStatus] = useState<StatusFilter>("all");
  const [format, setFormat] = useState("all");
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [confirmAction, setConfirmAction] = useState<"delete" | "reparse" | null>(null);
  const [busy, setBusy] = useState(false);

  // fetch wide (q only — status/format filter client-side over the page)
  const docs = useDocuments(
    q
      ? `?collection=${encodeURIComponent(collectionId)}&q=${encodeURIComponent(q)}&limit=200`
      : `?collection=${encodeURIComponent(collectionId)}&limit=200`,
  );

  const rows = useMemo(() => {
    let list = (docs.data ?? []) as DocumentRow[];
    if (status === "parsing") list = list.filter((d) => PARSING_STATES.has(d.state));
    else if (status !== "all") list = list.filter((d) => d.state === status);
    if (format !== "all") list = list.filter((d) => titleFormat(d.title) === format);
    return list;
  }, [docs.data, status, format]);

  const formats = useMemo(() => {
    const set = new Set<string>();
    for (const d of (docs.data ?? []) as DocumentRow[]) {
      const f = titleFormat(d.title);
      if (f !== "—") set.add(f);
    }
    return Array.from(set).sort();
  }, [docs.data]);

  const allSelected = rows.length > 0 && rows.every((d) => selected.has(d.id));
  const selectedInRows = rows.filter((d) => selected.has(d.id));

  function toggleAll() {
    setSelected(allSelected ? new Set() : new Set(rows.map((d) => d.id)));
  }

  function toggleOne(id: string) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  async function runBatch(action: "delete" | "reparse") {
    setBusy(true);
    try {
      const res = await adminFetch<{
        action: string;
        results: Array<{ doc_id: string; status: string; detail?: string }>;
      }>("/api/admin/v1/documents/batch", {
        method: "POST",
        body: JSON.stringify({ action, doc_ids: Array.from(selected) }),
      });
      const ok = res.results.filter((r) => r.status === "ok").length;
      const skipped = res.results.filter((r) => r.status === "skipped").length;
      const failed = res.results.filter((r) => r.status === "error");
      toast.success(`batch ${action}: ${ok} applied${skipped ? `, ${skipped} skipped` : ""}`, {
        description: failed.length
          ? `${failed.length} failed: ${failed[0].doc_id.slice(0, 8)}… ${failed[0].detail ?? ""}`
          : undefined,
      });
      setSelected(new Set());
      setConfirmAction(null);
      await docs.refetch();
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      toast.error(`batch ${action} rejected`, { description: msg });
    } finally {
      setBusy(false);
    }
  }

  async function deleteOne(id: string) {
    setBusy(true);
    try {
      await adminFetch(`/api/admin/v1/documents/${id}`, { method: "DELETE" });
      toast.success("document deleted");
      await docs.refetch();
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      toast.error("delete failed", { description: msg });
    } finally {
      setBusy(false);
    }
  }

  async function retryOne(id: string) {
    setBusy(true);
    try {
      await adminFetch(`/api/admin/v1/documents/${id}/retry`, {
        method: "POST",
        body: JSON.stringify({ scope: "shards" }),
      });
      toast.success("re-parse queued");
      await docs.refetch();
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      toast.error("retry failed", { description: msg });
    } finally {
      setBusy(false);
    }
  }

  if (docs.isPending) {
    return (
      <Card>
        <CardContent className="py-6 text-muted-foreground">loading documents…</CardContent>
      </Card>
    );
  }
  if (docs.isError) {
    return (
      <Card>
        <CardContent>
          <p className="text-redink">documents API unreachable: {docs.error.message}</p>
        </CardContent>
      </Card>
    );
  }

  return (
    <div className="flex flex-col gap-3">
      {/* filter & search bar */}
      <div className="flex flex-wrap items-center gap-2">
        <div className="relative">
          <Search
            className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-muted-foreground"
            aria-hidden="true"
          />
          <Input
            aria-label="search documents"
            placeholder="search titles…"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            className="w-56 pl-8"
          />
        </div>
        <Select value={status} onValueChange={(v) => setStatus(v as StatusFilter)}>
          <SelectTrigger aria-label="status filter" className="w-36">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {STATUS_FILTERS.map((s) => (
              <SelectItem key={s} value={s}>
                {s === "all" ? "all statuses" : s}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={format} onValueChange={setFormat}>
          <SelectTrigger aria-label="format filter" className="w-32">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">all formats</SelectItem>
            {formats.map((f) => (
              <SelectItem key={f} value={f}>
                {f}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {/* batch action bar — appears with a selection */}
      {selectedInRows.length > 0 && (
        <div className="flex flex-wrap items-center gap-2 rounded border border-press bg-press-wash px-3 py-2">
          <span className="text-[12.5px] font-medium">{selectedInRows.length} selected</span>
          <Button
            type="button"
            variant="outline"
            size="xs"
            disabled={busy}
            onClick={() => setConfirmAction("reparse")}
          >
            <RefreshCw aria-hidden="true" />
            Batch re-parse
          </Button>
          <Button
            type="button"
            variant="destructive"
            size="xs"
            disabled={busy}
            onClick={() => setConfirmAction("delete")}
          >
            <Trash2 aria-hidden="true" />
            Batch delete
          </Button>
          <Button type="button" variant="ghost" size="xs" onClick={() => setSelected(new Set())}>
            Clear
          </Button>
        </div>
      )}

      <Card>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="w-8">
                  <Checkbox
                    checked={
                      allSelected ? true : selectedInRows.length > 0 ? "indeterminate" : false
                    }
                    onCheckedChange={toggleAll}
                    aria-label="select all documents"
                  />
                </TableHead>
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  title
                </TableHead>
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  status
                </TableHead>
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  shards
                </TableHead>
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  pages
                </TableHead>
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  size
                </TableHead>
                <TableHead className="text-right text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  actions
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={7} className="py-8 text-center text-muted-foreground">
                    no documents match — ingest via Quick ingest above
                  </TableCell>
                </TableRow>
              ) : (
                rows.map((d) => (
                  <TableRow key={d.id} data-state={selected.has(d.id) ? "selected" : undefined}>
                    <TableCell>
                      <Checkbox
                        checked={selected.has(d.id)}
                        onCheckedChange={() => toggleOne(d.id)}
                        aria-label={`select ${d.title ?? d.id}`}
                      />
                    </TableCell>
                    <TableCell className="max-w-[280px]">
                      <button
                        type="button"
                        className="block max-w-full truncate text-left font-medium hover:text-press"
                        onClick={() => router.push(`/documents/${d.id}`)}
                      >
                        {d.title ?? "(untitled)"}
                      </button>
                    </TableCell>
                    <TableCell>
                      <StateBadge state={d.state} />
                    </TableCell>
                    <TableCell className="font-mono text-[12.5px] tabular-nums">
                      {d.shards_done}
                      {d.shards_failed > 0 ? (
                        <span className="text-redink">/{d.shards_failed}✗</span>
                      ) : null}
                      {d.total_shards != null ? (
                        <span className="text-muted-foreground">/{d.total_shards}</span>
                      ) : null}
                    </TableCell>
                    <TableCell className="tabular-nums">{d.page_count ?? "—"}</TableCell>
                    <TableCell className="font-mono text-[12.5px] tabular-nums">
                      {formatBytes(d.byte_size)}
                    </TableCell>
                    <TableCell className="text-right">
                      <div className="inline-flex items-center gap-1.5">
                        <Button
                          type="button"
                          variant="outline"
                          size="xs"
                          disabled={busy}
                          onClick={() => void retryOne(d.id)}
                        >
                          Retry
                        </Button>
                        <Button
                          type="button"
                          variant="ghost"
                          size="xs"
                          disabled={busy}
                          className="text-redink hover:text-redink"
                          onClick={() => void deleteOne(d.id)}
                        >
                          <Trash2 className="size-3.5" aria-hidden="true" />
                          <span className="sr-only">delete {d.title ?? d.id}</span>
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      {/* confirm dialog for batch actions */}
      <Dialog
        open={confirmAction !== null}
        onOpenChange={(open) => !open && !busy && setConfirmAction(null)}
      >
        <DialogContent showCloseButton={false}>
          <DialogHeader>
            <DialogTitle className="font-serif text-[17px] font-semibold">
              {confirmAction === "delete" ? "Batch delete" : "Batch re-parse"} —{" "}
              {selectedInRows.length} document{selectedInRows.length === 1 ? "" : "s"}
            </DialogTitle>
          </DialogHeader>
          <p className="m-0 text-[13px] text-muted-foreground">
            {confirmAction === "delete"
              ? "Deletes the documents, their shards and chunks. The raw MinIO objects stay for janitor GC. This cannot be undone."
              : "Requeues every failed shard of the selected documents through the retry ladder."}
          </p>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => setConfirmAction(null)}
              disabled={busy}
            >
              Cancel
            </Button>
            <Button
              type="button"
              variant={confirmAction === "delete" ? "destructive" : "default"}
              onClick={() => confirmAction && void runBatch(confirmAction)}
              disabled={busy}
            >
              {busy ? "Applying…" : confirmAction === "delete" ? "Delete" : "Re-parse"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
