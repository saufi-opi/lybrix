/** Upload (PRD §8.1 screen 4): presigned direct-to-MinIO, then commit. */

"use client";

import { useState } from "react";

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
      setStatus(["collection id is required"]);
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
      } catch (e) {
        log.push(`${file.name}: error ${e instanceof Error ? e.message : String(e)}`);
      }
      setStatus([...log]);
    }
    setBusy(false);
  }

  return (
    <div className="panel">
      <h2>Upload PDFs</h2>
      <p className="muted">
        Files go straight to MinIO via presigned URLs — the API never buffers bytes (§6.1).
      </p>
      <p>
        <input
          placeholder="collection id"
          value={collection}
          onChange={(e) => setCollection(e.target.value)}
        />{" "}
        <input
          placeholder="title (optional)"
          value={title}
          onChange={(e) => setTitle(e.target.value)}
        />
      </p>
      <input
        type="file"
        accept="application/pdf"
        multiple
        disabled={busy}
        onChange={(e) => onFiles(e.target.files)}
      />
      <ul>
        {status.map((line, i) => (
          // biome-ignore lint/suspicious/noArrayIndexKey: status lines are append-only log entries
          <li key={i} className="muted">
            {line}
          </li>
        ))}
      </ul>
    </div>
  );
}
