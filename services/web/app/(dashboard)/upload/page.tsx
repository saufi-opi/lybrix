"use client";

/** Upload hub (2.0.1): tabbed multi-source ingestion — Local files
 * (presigned direct-to-MinIO, parallel per-file hashing), Remote URL
 * (server-side streamed fetch with SSRF guard), OPDS connector
 * (Calibre-Web browse + sync). Feedback via sonner toasts + itemized
 * status lines. OPDS credentials are session-only — nothing persists. */

import { useCallback, useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useCollections } from "@/lib/queries";

async function sha256Hex(file: File): Promise<string> {
  const buf = await file.arrayBuffer();
  const digest = await crypto.subtle.digest("SHA-256", buf);
  return Array.from(new Uint8Array(digest))
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
}

async function adminFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: { "content-type": "application/json", ...(init?.headers ?? {}) },
  });
  const text = await res.text();
  if (!res.ok) {
    let detail = text.slice(0, 300);
    try {
      detail = (JSON.parse(text) as { detail?: string }).detail ?? detail;
    } catch {
      // keep raw text
    }
    throw new Error(`HTTP ${res.status}: ${detail}`);
  }
  if (res.status === 204) return undefined as T;
  return JSON.parse(text) as T;
}

interface LocalItem {
  name: string;
  state: "hashing" | "uploading" | "accepted" | "rejected" | "error";
  detail: string;
}

/** small concurrency limiter for parallel per-file uploads */
async function pooled<T>(items: T[], limit: number, fn: (item: T) => Promise<void>) {
  let next = 0;
  const workers = Array.from({ length: Math.min(limit, items.length) }, async () => {
    while (next < items.length) {
      const idx = next++;
      await fn(items[idx]);
    }
  });
  await Promise.all(workers);
}

const ACCEPTED_MIMES = "application/pdf,application/epub+zip";

export default function UploadPage() {
  const { data: collections } = useCollections();
  const collectionList = (collections ?? []) as Array<{ id: string; name: string }>;

  const [collection, setCollection] = useState("");
  const [title, setTitle] = useState("");
  const [localItems, setLocalItems] = useState<LocalItem[]>([]);
  const [busy, setBusy] = useState(false);

  // remote url tab
  const [urlText, setUrlText] = useState("");
  const [urlLines, setUrlLines] = useState<string[]>([]);

  // opds tab (session-only credentials)
  const [opdsUrl, setOpdsUrl] = useState("");
  const [opdsUser, setOpdsUser] = useState("");
  const [opdsPass, setOpdsPass] = useState("");
  const [opdsEntries, setOpdsEntries] = useState<
    Array<{
      title: string;
      authors: string[];
      summary?: string;
      acquisition: Array<{ href: string; mime_type: string }>;
    }>
  >([]);
  const [nextHref, setNextHref] = useState<string | null>(null);
  const [opdsPicked, setOpdsPicked] = useState<Set<string>>(new Set());
  const [opdsResults, setOpdsResults] = useState<
    Array<{ title: string; status: string; detail?: string }>
  >([]);

  const dropRef = useRef<HTMLDivElement>(null);
  const [dragOver, setDragOver] = useState(false);

  useEffect(() => {
    if (!collection && collectionList.length > 0) {
      setCollection(collectionList[0].id);
    }
  }, [collection, collectionList]);

  const updateItem = useCallback((name: string, patch: Partial<LocalItem>) => {
    setLocalItems((prev) => prev.map((it) => (it.name === name ? { ...it, ...patch } : it)));
  }, []);

  async function onFiles(files: FileList | File[] | null) {
    if (!files || files.length === 0) {
      toast.error("no files dropped");
      return;
    }
    if (!collection) {
      toast.error("pick a collection first");
      return;
    }
    setBusy(true);
    const arr = Array.from(files).filter(
      (f) =>
        f.type === "application/pdf" ||
        f.type === "application/epub+zip" ||
        /\.(pdf|epub)$/i.test(f.name),
    );
    if (arr.length === 0) {
      toast.error("only application/pdf and application/epub+zip files are accepted");
      setBusy(false);
      return;
    }
    setLocalItems(arr.map((f) => ({ name: f.name, state: "hashing" as const, detail: "" })));
    await pooled(arr, 3, async (file) => {
      try {
        updateItem(file.name, { state: "hashing" });
        const content_sha256 = await sha256Hex(file);
        updateItem(file.name, { state: "uploading", detail: "presigning…" });
        const presign = await adminFetch<{ doc_id: string; upload_url: string }>(
          "/api/admin/v1/documents/presign",
          {
            method: "POST",
            body: JSON.stringify({ collection_id: collection, byte_size: file.size }),
          },
        );
        await fetch(presign.upload_url, {
          method: "PUT",
          body: file,
          headers: {
            "content-type": file.type === "application/epub+zip" ? file.type : "application/pdf",
          },
        });
        updateItem(file.name, { detail: "committing…" });
        const commit = await fetch(`/api/admin/v1/documents/${presign.doc_id}/commit`, {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({
            collection_id: collection,
            content_sha256,
            title: title || file.name.replace(/\.(pdf|epub)$/i, ""),
          }),
        });
        if (commit.ok) {
          updateItem(file.name, { state: "accepted", detail: presign.doc_id });
          toast.success(`${file.name} accepted`, { description: presign.doc_id });
        } else {
          const t = await commit.text();
          updateItem(file.name, { state: "rejected", detail: `HTTP ${commit.status}` });
          toast.error(`${file.name} rejected`, { description: t.slice(0, 200) });
        }
      } catch (e) {
        const msg = e instanceof Error ? e.message : String(e);
        updateItem(file.name, { state: "error", detail: msg });
        toast.error(`${file.name} failed`, { description: msg });
      }
    });
    setBusy(false);
  }

  async function fetchUrls() {
    if (!collection) {
      toast.error("pick a collection first");
      return;
    }
    const urls = urlText
      .split("\n")
      .map((u) => u.trim())
      .filter(Boolean);
    if (urls.length === 0) return;
    setBusy(true);
    const log: string[] = [];
    for (const u of urls) {
      try {
        const res = await fetch("/api/admin/v1/documents/fetch-url", {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({
            collection_id: collection,
            url: u,
            title: title || undefined,
          }),
        });
        const bodyText = await res.text();
        let detail = bodyText;
        try {
          detail = (JSON.parse(bodyText) as { detail?: string }).detail ?? bodyText;
        } catch {
          // keep raw text
        }
        if (res.status === 202) {
          log.push(`${u}: accepted (${detail})`);
          toast.success(`URL accepted`, { description: u });
        } else {
          log.push(`${u}: ${res.status} ${detail}`);
          toast.error(`URL rejected (${res.status})`, { description: detail.slice(0, 200) });
        }
      } catch (e) {
        const msg = e instanceof Error ? e.message : String(e);
        log.push(`${u}: error ${msg}`);
        toast.error("fetch failed", { description: msg });
      }
      setUrlLines([...log]);
    }
    setBusy(false);
  }

  const browse = useCallback(
    async (feedUrl?: string) => {
      if (!opdsUrl) {
        toast.error("feed URL required");
        return;
      }
      setBusy(true);
      try {
        const res = await adminFetch<{
          entries: Array<{
            title: string;
            authors: string[];
            summary?: string;
            acquisition: Array<{ href: string; mime_type: string }>;
          }>;
          next_href: string | null;
        }>("/api/admin/v1/connectors/opds/browse", {
          method: "POST",
          body: JSON.stringify({
            url: opdsUrl,
            username: opdsUser,
            password: opdsPass,
            feed_url: feedUrl,
          }),
        });
        setOpdsEntries(res.entries);
        setNextHref(res.next_href);
        setOpdsPicked(new Set());
        toast.success(`${res.entries.length} entries`);
      } catch (e) {
        const msg = e instanceof Error ? e.message : String(e);
        toast.error("browse failed", { description: msg });
      } finally {
        setBusy(false);
      }
    },
    [opdsUrl, opdsUser, opdsPass],
  );

  const syncSelection = useCallback(
    async (all: boolean) => {
      if (!collection) {
        toast.error("pick a collection first");
        return;
      }
      const selection = opdsEntries
        .filter((e) => {
          const key = e.acquisition[0]?.href ?? `${e.title}:${e.authors.join(",")}`;
          return all || opdsPicked.has(key);
        })
        .flatMap((e) => {
          const acq =
            e.acquisition.find(
              (a) => a.mime_type === "application/pdf" || a.mime_type === "application/epub+zip",
            ) ?? e.acquisition[0];
          return acq ? [{ title: e.title, href: acq.href, mime_type: acq.mime_type }] : [];
        });
      if (selection.length === 0) {
        toast.error("nothing selected with an acquisition link");
        return;
      }
      setBusy(true);
      try {
        const res = await adminFetch<{
          synced: number;
          failed: number;
          results: Array<{ title: string; status: string; detail?: string }>;
        }>("/api/admin/v1/connectors/opds/sync", {
          method: "POST",
          body: JSON.stringify({
            url: opdsUrl,
            username: opdsUser,
            password: opdsPass,
            collection_id: collection,
            selection,
          }),
        });
        setOpdsResults(res.results);
        toast.success(`synced ${res.synced}, failed ${res.failed}`);
      } catch (e) {
        const msg = e instanceof Error ? e.message : String(e);
        toast.error("sync failed", { description: msg });
      } finally {
        setBusy(false);
      }
    },
    [collection, opdsEntries, opdsPicked, opdsUrl, opdsUser, opdsPass],
  );

  const dropzoneProps = {
    onDragOver: (e: React.DragEvent) => {
      e.preventDefault();
      setDragOver(true);
    },
    onDragLeave: () => setDragOver(false),
    onDrop: (e: React.DragEvent) => {
      e.preventDefault();
      setDragOver(false);
      void onFiles(e.dataTransfer.files);
    },
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-serif text-[19px] font-semibold">Ingest sources</CardTitle>
        <CardDescription>
          Local files upload via presigned URLs — the API never buffers bytes. Remote URLs and OPDS
          feeds are fetched server-side into MinIO.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <div className="flex flex-wrap gap-3">
          <div className="flex flex-col gap-1.5">
            <Label className="text-xs font-semibold text-muted-foreground">Collection</Label>
            <Select value={collection} onValueChange={setCollection}>
              <SelectTrigger aria-label="collection" className="w-64">
                <SelectValue placeholder="pick a collection" />
              </SelectTrigger>
              <SelectContent>
                {collectionList.map((c) => (
                  <SelectItem key={c.id} value={c.id}>
                    {c.name} ({c.id})
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="upload-title" className="text-xs font-semibold text-muted-foreground">
              title override (optional)
            </Label>
            <Input
              id="upload-title"
              placeholder="title override (optional)"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              className="w-56"
            />
          </div>
        </div>

        <Tabs defaultValue="local">
          <TabsList>
            <TabsTrigger value="local">Local files</TabsTrigger>
            <TabsTrigger value="url">Remote URL</TabsTrigger>
            <TabsTrigger value="opds">OPDS</TabsTrigger>
          </TabsList>

          {/* ===== tab 1: local files ===== */}
          <TabsContent value="local" className="flex flex-col gap-3">
            <div
              ref={dropRef}
              {...dropzoneProps}
              className={
                "flex min-h-[120px] flex-col items-center justify-center gap-1.5 rounded-[4px] border-2 border-dashed px-4 py-6 text-center transition-colors duration-300 " +
                (dragOver
                  ? "border-press bg-press-wash"
                  : "border-sheet-edge hover:border-ink-faint")
              }
            >
              <p className="m-0 font-medium">Drop PDFs or EPUBs here</p>
              <p className="m-0 text-[12.5px] text-muted-foreground">
                or browse below — multiple files upload in parallel
              </p>
              <Input
                type="file"
                accept={ACCEPTED_MIMES}
                multiple
                disabled={busy}
                className="cursor-pointer"
                onChange={(e) => onFiles(e.target.files)}
              />
            </div>
            {localItems.length > 0 && (
              <ul className="m-0 list-none space-y-1 font-mono text-xs">
                {localItems.map((it) => (
                  <li key={it.name} className="flex items-center gap-2">
                    <span className="min-w-[9ch]">{it.state}</span>
                    <span>{it.name}</span>
                    {it.detail && <span className="text-muted-foreground">{it.detail}</span>}
                  </li>
                ))}
              </ul>
            )}
            {busy && (
              <Button type="button" disabled className="w-fit">
                Uploading…
              </Button>
            )}
          </TabsContent>

          {/* ===== tab 2: remote url ===== */}
          <TabsContent value="url" className="flex flex-col gap-3">
            <Label htmlFor="url-text" className="text-xs font-semibold text-muted-foreground">
              PDF/EPUB URLs — one per line
            </Label>
            <textarea
              id="url-text"
              rows={4}
              value={urlText}
              onChange={(e) => setUrlText(e.target.value)}
              placeholder={"https://arxiv.org/pdf/2401.00000\nhttps://example.com/book.epub"}
              className="min-h-[96px] w-full rounded-[4px] border border-sheet-edge bg-sheet px-3 py-2 font-mono text-[12.5px]"
            />
            <Button type="button" onClick={fetchUrls} disabled={busy} className="w-fit">
              Fetch {urlText.split("\n").filter((u) => u.trim()).length || ""} URL(s)
            </Button>
            {urlLines.length > 0 && (
              <ul className="m-0 list-none space-y-0.5 font-mono text-xs text-muted-foreground">
                {urlLines.map((line) => (
                  <li key={line}>{line}</li>
                ))}
              </ul>
            )}
          </TabsContent>

          {/* ===== tab 3: opds ===== */}
          <TabsContent value="opds" className="flex flex-col gap-3">
            <div className="grid grid-cols-1 gap-3 min-[640px]:grid-cols-4">
              <div className="flex flex-col gap-1.5 min-[640px]:col-span-2">
                <Label htmlFor="opds-url" className="text-xs font-semibold text-muted-foreground">
                  OPDS feed URL
                </Label>
                <Input
                  id="opds-url"
                  placeholder="http://calibre-web:8083/opds"
                  value={opdsUrl}
                  onChange={(e) => setOpdsUrl(e.target.value)}
                />
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="opds-user" className="text-xs font-semibold text-muted-foreground">
                  Username
                </Label>
                <Input
                  id="opds-user"
                  autoComplete="off"
                  value={opdsUser}
                  onChange={(e) => setOpdsUser(e.target.value)}
                />
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="opds-pass" className="text-xs font-semibold text-muted-foreground">
                  Password
                </Label>
                <Input
                  id="opds-pass"
                  type="password"
                  autoComplete="off"
                  value={opdsPass}
                  onChange={(e) => setOpdsPass(e.target.value)}
                />
              </div>
            </div>
            <p className="m-0 text-[11.5px] text-muted-foreground">
              Credentials stay in this browser tab — nothing is persisted server-side.
            </p>
            <div className="flex flex-wrap gap-2">
              <Button type="button" onClick={() => browse()} disabled={busy} className="w-fit">
                Browse
              </Button>
              <Button
                type="button"
                variant="outline"
                onClick={() => syncSelection(false)}
                disabled={busy || opdsEntries.length === 0}
                className="w-fit"
              >
                Sync selection
              </Button>
              <Button
                type="button"
                variant="outline"
                onClick={() => syncSelection(true)}
                disabled={busy || opdsEntries.length === 0}
                className="w-fit"
              >
                Sync all on this page
              </Button>
              {nextHref && (
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => browse(nextHref)}
                  disabled={busy}
                  className="w-fit"
                >
                  Next page
                </Button>
              )}
            </div>
            {opdsEntries.length > 0 && (
              <Table>
                <TableHeader>
                  <TableRow className="hover:bg-transparent">
                    <TableHead className="w-8" />
                    <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                      title
                    </TableHead>
                    <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                      author
                    </TableHead>
                    <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                      formats
                    </TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {opdsEntries.map((e) => {
                    const key = e.acquisition[0]?.href ?? `${e.title}:${e.authors.join(",")}`;
                    return (
                      <TableRow key={key}>
                        <TableCell>
                          <input
                            type="checkbox"
                            checked={opdsPicked.has(key)}
                            onChange={() =>
                              setOpdsPicked((prev) => {
                                const next = new Set(prev);
                                if (next.has(key)) next.delete(key);
                                else next.add(key);
                                return next;
                              })
                            }
                            className="size-[15px] cursor-pointer accent-[var(--press)]"
                            aria-label={`select ${e.title}`}
                          />
                        </TableCell>
                        <TableCell className="font-medium">{e.title}</TableCell>
                        <TableCell className="text-muted-foreground">
                          {e.authors.join(", ")}
                        </TableCell>
                        <TableCell className="font-mono text-[11.5px]">
                          {e.acquisition
                            .map((a) => a.mime_type.replace("application/", ""))
                            .join(", ")}
                        </TableCell>
                      </TableRow>
                    );
                  })}
                </TableBody>
              </Table>
            )}
            {opdsResults.length > 0 && (
              <div className="flex flex-col gap-1">
                <p className="m-0 text-[11.5px] font-semibold tracking-[0.02em] text-muted-foreground">
                  sync results
                </p>
                <ul className="m-0 list-none space-y-0.5 font-mono text-xs">
                  {opdsResults.map((r) => (
                    <li key={r.title}>
                      {r.status}: {r.title}
                      {r.detail && <span className="text-muted-foreground"> — {r.detail}</span>}
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </TabsContent>
        </Tabs>
      </CardContent>
    </Card>
  );
}
