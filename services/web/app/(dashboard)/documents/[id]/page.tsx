"use client";

/** Document detail (PRD §8.1 screen 3 / Workstream 3): tabbed — Overview
 * (header card, shard grid, shard table) and Chunk Inspector (paginated
 * chunk table with breadcrumbs, parent marking, expandable rows). Client
 * component — useDocument()/useShards()/useChunks() refetch on focus, so a
 * retry shows the grid flip without a page reload. The chunk table body is
 * shared with the KB detail tab via components/chunk-inspector.tsx. */

import { useParams } from "next/navigation";
import { useState } from "react";
import { CHUNKS_PAGE_SIZE, ChunkInspector } from "@/components/chunk-inspector";
import { RetryButtons } from "@/components/retry-buttons";
import { ShardGrid } from "@/components/shard-grid";
import { StateBadge } from "@/components/state-badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import type { ShardRow } from "@/lib/api-client";
import { useChunks, useDocument, useShards } from "@/lib/queries";

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

function DocumentDetail({ id }: { id: string }) {
  const doc = useDocument(id);
  const shards = useShards(id);
  const [page, setPage] = useState(1);

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
          <DocChunkPanel id={id} page={page} onPage={setPage} />
        </TabsContent>
      </Tabs>
    </>
  );
}

/** per-doc chunk panel — the shared ChunkInspector with the per-doc hook. */
function DocChunkPanel({
  id,
  page,
  onPage,
}: {
  id: string;
  page: number;
  onPage: (p: number) => void;
}) {
  const chunks = useChunks(id, page, CHUNKS_PAGE_SIZE);
  return (
    <ChunkInspector
      title="chunks"
      total={chunks.data?.total ?? 0}
      page={page}
      totalPages={Math.max(1, Math.ceil((chunks.data?.total ?? 0) / CHUNKS_PAGE_SIZE))}
      onPage={onPage}
      chunks={chunks.data?.chunks ?? []}
      isPending={chunks.isPending}
      isError={chunks.isError}
      errorMessage={chunks.error?.message}
    />
  );
}

export default function DocumentDetailPage() {
  const params = useParams<{ id: string }>();
  return <DocumentDetail id={params.id} />;
}
