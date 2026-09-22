"use client";

/** Chunk Inspector table — extracted from document detail (Workstream 3) so
 * the doc-detail page and the KB-wide Chunk Inspector tab share it (Phase 3
 * Tab 2). Seq-ordered paginated chunk table: parent rows visually marked,
 * rows expand to full text, parent-child link jumps selection to the parent
 * row (or notes it as off-page). */

import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import type { ChunkRow } from "@/lib/api-client";

const CHUNKS_PAGE_SIZE = 50;

export { CHUNKS_PAGE_SIZE };

/** Presentational chunk table. Pagination + data fetching live with the
 * caller (doc-detail uses useChunks; the KB tab uses the collection-wide
 * endpoint), so this component only renders rows. */
export function ChunkTable({
  chunks,
  expanded,
  onToggle,
}: {
  chunks: ChunkRow[];
  expanded: string | null;
  onToggle: (id: string | null) => void;
}) {
  const byId = new Map(chunks.map((c) => [c.id, c]));
  return (
    <Table>
      <TableHeader>
        <TableRow className="hover:bg-transparent">
          <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
            seq
          </TableHead>
          <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
            pages
          </TableHead>
          <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
            tokens
          </TableHead>
          <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
            breadcrumb
          </TableHead>
          <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
            text
          </TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {chunks.map((c) => {
          const isExpanded = expanded === c.id;
          const breadcrumb = (c.heading_path ?? []).join(" > ") || c.header_breadcrumb || "—";
          return (
            <TableRow
              key={c.id}
              className={`cursor-pointer ${c.is_parent ? "bg-paper-deep/60 font-medium" : ""}`}
              onClick={() => onToggle(isExpanded ? null : c.id)}
            >
              <TableCell className="tabular-nums">
                {c.seq}
                {c.is_parent && (
                  <span
                    className="ml-1.5 rounded-[2px] border border-press px-1 font-mono text-[0.62rem] font-semibold text-press-deep"
                    title="parent chunk (breadcrumb carrier, not embedded)"
                  >
                    P
                  </span>
                )}
              </TableCell>
              <TableCell className="tabular-nums">
                {c.page_start != null ? `${c.page_start}–${c.page_end ?? c.page_start}` : "—"}
              </TableCell>
              <TableCell className="tabular-nums">{c.token_count}</TableCell>
              <TableCell className="max-w-[200px] truncate font-mono text-[11.5px] text-muted-foreground">
                {breadcrumb}
              </TableCell>
              <TableCell className="max-w-[420px]">
                {isExpanded ? (
                  <div className="text-[12.5px] break-words whitespace-pre-wrap">
                    {c.text}
                    {c.parent_id && byId.has(c.parent_id) && (
                      <p className="mt-2 text-[11.5px] text-muted-foreground">
                        parent:{" "}
                        <button
                          type="button"
                          className="font-mono text-press underline decoration-dotted"
                          onClick={(e) => {
                            e.stopPropagation();
                            onToggle(c.parent_id as string);
                          }}
                        >
                          seq {byId.get(c.parent_id as string)?.seq}
                        </button>
                      </p>
                    )}
                    {c.parent_id && !byId.has(c.parent_id) && (
                      <p className="mt-2 font-mono text-[11.5px] text-muted-foreground">
                        parent {c.parent_id.slice(0, 8)}… (off-page)
                      </p>
                    )}
                  </div>
                ) : (
                  <span className="line-clamp-2 text-[12.5px] text-muted-foreground">{c.text}</span>
                )}
              </TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}

/** Full inspector panel with its own pagination — the document-detail shape
 * (data via the per-doc chunks hook passed in by the parent). */
export function ChunkInspector({
  title,
  total,
  page,
  totalPages,
  onPage,
  chunks,
  isPending,
  isError,
  errorMessage,
}: {
  title: React.ReactNode;
  total: number;
  page: number;
  totalPages: number;
  onPage: (page: number) => void;
  chunks: ChunkRow[];
  isPending: boolean;
  isError: boolean;
  errorMessage?: string;
}) {
  const [expanded, setExpanded] = useState<string | null>(null);

  if (isPending) {
    return (
      <Card>
        <CardContent className="flex flex-col gap-2 py-6">
          <Skeleton className="h-6 w-full" />
          <Skeleton className="h-6 w-full" />
          <Skeleton className="h-6 w-2/3" />
        </CardContent>
      </Card>
    );
  }
  if (isError) {
    return (
      <Card>
        <CardContent>
          <p className="text-redink">chunks API unreachable: {errorMessage}</p>
        </CardContent>
      </Card>
    );
  }

  return (
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <CardTitle>
            {title} <span className="text-muted-foreground">({total})</span>
          </CardTitle>
          <div className="flex items-center gap-2">
            <Button
              type="button"
              variant="outline"
              size="xs"
              disabled={page <= 1}
              onClick={() => onPage(Math.max(1, page - 1))}
            >
              ← Prev
            </Button>
            <span className="font-mono text-xs text-muted-foreground tabular-nums">
              {page} / {totalPages}
            </span>
            <Button
              type="button"
              variant="outline"
              size="xs"
              disabled={page >= totalPages}
              onClick={() => onPage(Math.min(totalPages, page + 1))}
            >
              Next →
            </Button>
          </div>
        </div>
      </CardHeader>
      <CardContent>
        <ChunkTable chunks={chunks} expanded={expanded} onToggle={setExpanded} />
      </CardContent>
    </Card>
  );
}
