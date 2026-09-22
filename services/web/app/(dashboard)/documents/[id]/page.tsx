"use client";

/** Document detail (PRD §8.1 screen 3 / Workstream 3): tabbed — Overview
 * (header card, shard grid, shard table) and Chunk Inspector (paginated
 * chunk table with breadcrumbs, parent marking, expandable rows). Client
 * component — useDocument()/useShards()/useChunks() refetch on focus, so a
 * retry shows the grid flip without a page reload. */

import { useParams } from "next/navigation";
import { useState } from "react";
import { RetryButtons } from "@/components/retry-buttons";
import { ShardGrid } from "@/components/shard-grid";
import { StateBadge } from "@/components/state-badge";
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import type { ChunkRow, ShardRow } from "@/lib/api-client";
import { useChunks, useDocument, useShards } from "@/lib/queries";

const CHUNKS_PAGE_SIZE = 50;

function Overview({ id, shards }: { id: string; shards: ShardRow[] }) {
  return (
    <>
      <Card>
        <CardHeader>
          <CardTitle>Shard grid</CardTitle>
        </CardHeader>
        <CardContent>
          <ShardGrid shards={shards} />
        </CardContent>
      </Card>

      <Card className="mt-4">
        <CardHeader>
          <CardTitle>Shards</CardTitle>
        </CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  idx
                </TableHead>
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  pages
                </TableHead>
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  state
                </TableHead>
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  attempts
                </TableHead>
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  duration
                </TableHead>
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  peak RSS
                </TableHead>
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  error
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {shards.map((s) => (
                <TableRow key={s.idx}>
                  <TableCell className="tabular-nums">{s.idx}</TableCell>
                  <TableCell className="tabular-nums">
                    {s.page_start}–{s.page_end}
                  </TableCell>
                  <TableCell>
                    <StateBadge state={s.state} />
                  </TableCell>
                  <TableCell className="tabular-nums">{s.attempts}</TableCell>
                  <TableCell className="tabular-nums">
                    {s.duration_ms != null ? `${(s.duration_ms / 1000).toFixed(1)}s` : "—"}
                  </TableCell>
                  <TableCell className="tabular-nums">
                    {s.peak_rss_mb != null ? `${s.peak_rss_mb}MB` : "—"}
                  </TableCell>
                  <TableCell className="text-muted-foreground">{s.error_code ?? "—"}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>
      <span className="hidden">{id}</span>
    </>
  );
}

/** Chunk Inspector (Workstream 3): seq-ordered paginated chunk table.
 * Parent rows are visually marked; a row expands to its full text plus a
 * parent-child link that jumps the selection to the parent row. */
function ChunkInspector({ id }: { id: string }) {
  const [page, setPage] = useState(1);
  const [expanded, setExpanded] = useState<string | null>(null);
  const chunks = useChunks(id, page, CHUNKS_PAGE_SIZE);

  if (chunks.isPending) {
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
  if (chunks.isError) {
    return (
      <Card>
        <CardContent>
          <p className="text-redink">chunks API unreachable: {chunks.error.message}</p>
        </CardContent>
      </Card>
    );
  }

  const { total, chunks: rows } = chunks.data as { total: number; chunks: ChunkRow[] };
  const totalPages = Math.max(1, Math.ceil(total / CHUNKS_PAGE_SIZE));
  const byId = new Map(rows.map((c) => [c.id, c]));

  return (
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <CardTitle>
            Chunks <span className="text-muted-foreground">({total})</span>
          </CardTitle>
          <div className="flex items-center gap-2">
            <Button
              type="button"
              variant="outline"
              size="xs"
              disabled={page <= 1}
              onClick={() => setPage((p) => Math.max(1, p - 1))}
            >
              ← Prev
            </Button>
            <span className="font-mono text-xs tabular-nums text-muted-foreground">
              {page} / {totalPages}
            </span>
            <Button
              type="button"
              variant="outline"
              size="xs"
              disabled={page >= totalPages}
              onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
            >
              Next →
            </Button>
          </div>
        </div>
      </CardHeader>
      <CardContent>
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
            {rows.map((c) => {
              const isExpanded = expanded === c.id;
              const breadcrumb = (c.heading_path ?? []).join(" > ") || c.header_breadcrumb || "—";
              return (
                <TableRow
                  key={c.id}
                  className={`cursor-pointer ${c.is_parent ? "bg-paper-deep/60 font-medium" : ""}`}
                  onClick={() => setExpanded(isExpanded ? null : c.id)}
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
                      <div className="whitespace-pre-wrap break-words text-[12.5px]">
                        {c.text}
                        {c.parent_id && byId.has(c.parent_id) && (
                          <p className="mt-2 text-[11.5px] text-muted-foreground">
                            parent:{" "}
                            <button
                              type="button"
                              className="font-mono text-press underline decoration-dotted"
                              onClick={(e) => {
                                e.stopPropagation();
                                setExpanded(c.parent_id as string);
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
                      <span className="line-clamp-2 text-[12.5px] text-muted-foreground">
                        {c.text}
                      </span>
                    )}
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  );
}

function DocumentDetail({ id }: { id: string }) {
  const doc = useDocument(id);
  const shards = useShards(id);

  if (doc.isPending || shards.isPending) {
    return <p className="text-muted-foreground">loading document…</p>;
  }
  if (doc.isError) {
    return <p className="text-redink">document API unreachable: {doc.error.message}</p>;
  }
  if (shards.isError) {
    return <p className="text-redink">shards API unreachable: {shards.error.message}</p>;
  }

  const d = doc.data;
  const rows = shards.data;

  return (
    <>
      <Card>
        <CardHeader>
          <CardTitle className="font-serif text-[19px] font-semibold">{d.title ?? id}</CardTitle>
        </CardHeader>
        <CardContent>
          <p className="m-0 flex flex-wrap items-center gap-2">
            <StateBadge state={d.state} />
            {d.completeness != null && (
              <span className="text-muted-foreground">
                · completeness {Math.round(d.completeness * 100)}%
              </span>
            )}
            {d.error_code && (
              <span className="text-muted-foreground">· last error {d.error_code}</span>
            )}
          </p>
          <div className="mt-3">
            <RetryButtons docId={id} />
          </div>
        </CardContent>
      </Card>

      <Tabs defaultValue="overview" className="mt-4">
        <TabsList>
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="chunks">Chunk Inspector</TabsTrigger>
        </TabsList>
        <TabsContent value="overview" className="mt-4">
          <Overview id={id} shards={rows} />
        </TabsContent>
        <TabsContent value="chunks" className="mt-4">
          <ChunkInspector id={id} />
        </TabsContent>
      </Tabs>
    </>
  );
}

export default function DocumentDetailPage() {
  const params = useParams<{ id: string }>();
  return <DocumentDetail id={params.id} />;
}
