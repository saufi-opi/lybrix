"use client";

/** Upload (PRD §8.1 screen 4): presigned direct-to-MinIO, then commit.
 * The browser computes SHA-256 and PUTs straight to MinIO — the API never
 * buffers bytes (§6.1). Feedback via sonner toasts + an inline log. */

import { useState } from "react";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

async function sha256Hex(file: File): Promise<string> {
  const buf = await file.arrayBuffer();
  const digest = await crypto.subtle.digest("SHA-256", buf);
  return Array.from(new Uint8Array(digest))
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
}

export default function UploadPage() {
  const [collection, setCollection] = useState("");
  const [title, setTitle] = useState("");
  const [status, setStatus] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);

  async function onFiles(files: FileList | null) {
    if (!files || !collection) {
      toast.error("collection id is required");
      return;
    }
    setBusy(true);
    const log: string[] = [];
    for (const file of Array.from(files)) {
      try {
        log.push(`${file.name}: hashing…`);
        setStatus([...log]);
        const content_sha256 = await sha256Hex(file);
        const presign = await fetch(`/api/admin/v1/documents/presign`, {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ collection_id: collection, byte_size: file.size }),
        }).then((r) => r.json());

        await fetch(presign.upload_url, {
          method: "PUT",
          body: file,
          headers: { "content-type": "application/pdf" },
        });

        const commit = await fetch(`/api/admin/v1/documents/${presign.doc_id}/commit`, {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({
            collection_id: collection,
            content_sha256,
            title: title || file.name.replace(/\.pdf$/i, ""),
          }),
        });
        log.push(
          commit.ok
            ? `${file.name}: accepted (${presign.doc_id})`
            : `${file.name}: rejected (${commit.status})`,
        );
        if (commit.ok) {
          toast.success(`${file.name} accepted`, { description: presign.doc_id });
        } else {
          toast.error(`${file.name} rejected`, { description: `HTTP ${commit.status}` });
        }
      } catch (e) {
        const msg = e instanceof Error ? e.message : String(e);
        log.push(`${file.name}: error ${msg}`);
        toast.error(`${file.name} failed`, { description: msg });
      }
      setStatus([...log]);
    }
    setBusy(false);
  }

  return (
    <Card className="max-w-2xl">
      <CardHeader>
        <CardTitle className="font-serif text-[19px] font-semibold">Upload PDFs</CardTitle>
        <CardDescription>
          Files go straight to MinIO via presigned URLs — the API never buffers bytes (§6.1).
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        <div className="flex flex-wrap gap-2">
          <div className="flex flex-col gap-1">
            <Label htmlFor="upload-collection" className="text-xs text-muted-foreground">
              collection id
            </Label>
            <Input
              id="upload-collection"
              placeholder="collection id"
              value={collection}
              onChange={(e) => setCollection(e.target.value)}
              className="w-56"
            />
          </div>
          <div className="flex flex-col gap-1">
            <Label htmlFor="upload-title" className="text-xs text-muted-foreground">
              title (optional)
            </Label>
            <Input
              id="upload-title"
              placeholder="title (optional)"
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              className="w-56"
            />
          </div>
        </div>
        <Input
          type="file"
          accept="application/pdf"
          multiple
          disabled={busy}
          onChange={(e) => onFiles(e.target.files)}
          className="cursor-pointer py-1.5"
        />
        {status.length > 0 && (
          <ul className="m-0 list-none space-y-0.5 font-mono text-xs text-muted-foreground">
            {status.map((line, i) => (
              // biome-ignore lint/suspicious/noArrayIndexKey: status lines are append-only log entries
              <li key={i}>{line}</li>
            ))}
          </ul>
        )}
        {busy && (
          <Button type="button" disabled className="w-fit">
            Uploading…
          </Button>
        )}
      </CardContent>
    </Card>
  );
}
