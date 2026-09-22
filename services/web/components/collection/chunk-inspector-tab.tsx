"use client";

/** KB Chunk Inspector tab (WeKnora Tab 2): KB-wide paginated chunk table via
 * GET /v1/collections/{id}/chunks, narrowed per document with a doc picker;
 * breadcrumb navigation and parent-child links come from the shared
 * ChunkTable. */

import { useState } from "react";
import { CHUNKS_PAGE_SIZE, ChunkInspector } from "@/components/chunk-inspector";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import type { DocumentRow } from "@/lib/api-client";
import { useCollectionChunks } from "@/lib/queries";

export function ChunkInspectorTab({
  collectionId,
  documents,
}: {
  collectionId: string;
  documents: DocumentRow[];
}) {
  const [docId, setDocId] = useState<string>("all");
  const [page, setPage] = useState(1);
  const narrowed = docId === "all" ? undefined : docId;
  const chunks = useCollectionChunks(collectionId, narrowed, page, CHUNKS_PAGE_SIZE);

  const selectedDoc = documents.find((d) => d.id === docId);

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <Select
          value={docId}
          onValueChange={(v) => {
            setDocId(v);
            setPage(1);
          }}
        >
          <SelectTrigger aria-label="narrow to document" className="w-72">
            <SelectValue placeholder="all documents" />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">all documents</SelectItem>
            {documents.map((d) => (
              <SelectItem key={d.id} value={d.id}>
                {d.title ?? d.id}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {selectedDoc?.error_code && (
          <span className="text-[11.5px] text-redink">last error: {selectedDoc.error_code}</span>
        )}
      </div>

      <ChunkInspector
        title={
          selectedDoc ? `chunks — ${selectedDoc.title ?? docId.slice(0, 8)}` : "chunks (KB-wide)"
        }
        total={chunks.data?.total ?? 0}
        page={page}
        totalPages={Math.max(1, Math.ceil((chunks.data?.total ?? 0) / CHUNKS_PAGE_SIZE))}
        onPage={setPage}
        chunks={chunks.data?.chunks ?? []}
        isPending={chunks.isPending}
        isError={chunks.isError}
        errorMessage={chunks.error?.message}
      />
    </div>
  );
}
