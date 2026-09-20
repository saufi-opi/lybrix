"use client";

/** Document detail (PRD §8.1 screen 3): header card, shard grid, retry buttons.
 * Client component — useDocument()/useShards() refetch on focus, so a retry
 * shows the grid flip without a page reload. */

import { useParams } from "next/navigation";
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
import { useDocument, useShards } from "@/lib/queries";

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

      <Card className="mt-4">
        <CardHeader>
          <CardTitle>Shard grid</CardTitle>
        </CardHeader>
        <CardContent>
          <ShardGrid shards={rows} />
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
              {rows.map((s) => (
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
    </>
  );
}

export default function DocumentDetailPage() {
  const params = useParams<{ id: string }>();
  return <DocumentDetail id={params.id} />;
}
