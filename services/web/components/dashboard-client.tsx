"use client";

/** Dashboard live data (PRD §8.1 screen 1): stat cards, queue table,
 * dependency chips. Polls every 5s via the lib/queries.ts hooks. */

import { cn } from "cn";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useHealth, usePipeline, useQueues } from "@/lib/queries";

type Queues = Awaited<ReturnType<typeof useQueues>>["data"];

/** Stat numeral: mono, tabular, sitting on the paper under one rule —
 * the "open counter" treatment, not a boxed card. */
function Stat({ num, label }: { num: React.ReactNode; label: string }) {
  return (
    <div className="border-b border-sheet-edge pb-4 pt-1.5">
      <div className="font-mono text-2xl font-medium leading-[1.1] tracking-[-0.02em] tabular-nums">
        {num}
      </div>
      <div className="mt-1 text-xs text-muted-foreground">{label}</div>
    </div>
  );
}

export function DashboardClient() {
  const pipeline = usePipeline();
  const queues = useQueues();
  const health = useHealth();

  if (pipeline.isPending || queues.isPending || health.isPending) {
    return <p className="text-muted-foreground">loading dashboard…</p>;
  }
  if (pipeline.isError || queues.isError || health.isError) {
    return (
      <p className="text-redink">
        control-plane API unreachable:{" "}
        {pipeline.error?.message ?? queues.error?.message ?? health.error?.message}
      </p>
    );
  }

  const c = (pipeline.data.counts ?? {}) as Record<string, unknown>;
  // Authoritative per-state counts from GROUP BY state (server-side aggregate).
  const states = (c.docs_states ?? {}) as Record<string, number>;
  const ready = states.ready ?? 0;
  const failed = (c.docs_failed as number) ?? 0;
  const partial = states.partial ?? 0;
  // Phase breakdown of the old ambiguous "in-flight" card (2026-09-12):
  // parsing_active = shards still converting; awaiting_embed = whole-book
  // settled, waiting on the embedder. Both are non-terminal doc counts —
  // job-level in-flight (Redis PEL) lives on the pipeline page.
  const parsingActive = (c.docs_parsing_active as number) ?? null;
  const awaitingEmbed = (c.docs_awaiting_embed as number) ?? null;
  const activeDocs =
    (states.uploaded ?? 0) +
    (states.splitting ?? 0) +
    (states.parsing ?? 0) +
    (states.embedding ?? 0) +
    (states.indexing ?? 0);
  const total = (c.docs_total as number) ?? 0;

  return (
    <>
      <div className="grid grid-cols-2 gap-x-6 max-[900px]:grid-cols-2 md:grid-cols-4">
        <Stat num={ready} label={`ready${total ? ` / ${total}` : ""}`} />
        <Stat
          num={
            parsingActive !== null ? (
              <>
                {parsingActive}
                <span className="text-[0.55em] text-muted-foreground">
                  {" "}
                  + {awaitingEmbed ?? 0} queued
                </span>
              </>
            ) : (
              activeDocs
            )
          }
          label="parsing active (+ awaiting embed)"
        />
        <Stat num={failed} label="failed" />
        <Stat num={partial} label="partial" />
      </div>

      <Card className="mt-6">
        <CardHeader>
          <CardTitle>Queue depth</CardTitle>
        </CardHeader>
        <CardContent>
          <QueueTable queues={queues.data} />
        </CardContent>
      </Card>

      <Card className="mt-4">
        <CardHeader>
          <CardTitle>Dependencies</CardTitle>
        </CardHeader>
        <CardContent>
          <p className="m-0 flex flex-wrap gap-2.5">
            {Object.entries(health.data).map(([k, v]) => (
              <span
                key={k}
                className="inline-flex items-center gap-1.5 rounded border border-sheet-edge bg-paper-deep px-2.5 py-1 font-mono text-xs"
              >
                <span
                  className={cn("size-[7px] rounded-full", v === "ok" ? "bg-press" : "bg-redink")}
                />
                <b className="font-semibold text-ink">{k}</b> {v}
              </span>
            ))}
          </p>
        </CardContent>
      </Card>
    </>
  );
}

function QueueTable({ queues }: { queues: NonNullable<Queues> }) {
  return (
    <Table>
      <TableHeader>
        <TableRow className="hover:bg-transparent">
          <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
            stream
          </TableHead>
          <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
            undelivered
          </TableHead>
          <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
            pending (PEL)
          </TableHead>
          <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
            history (XLEN)
          </TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {Object.entries(queues).map(([name, q]) => (
          <TableRow key={name}>
            <TableCell className="font-medium">{name}</TableCell>
            <TableCell className="tabular-nums">{q.undelivered ?? "—"}</TableCell>
            <TableCell className="tabular-nums">{q.pending ?? "—"}</TableCell>
            <TableCell className="text-muted-foreground tabular-nums">{q.length ?? "—"}</TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}
