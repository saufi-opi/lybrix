"use client";

/** Pipeline live view (2026-09-10): component strip, queue lanes end-to-end,
 *  in-flight jobs. Polls /v1/system/pipeline every 5s via usePipeline(). */

import { cn } from "cn";
import { useEffect, useRef, useState } from "react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { usePipeline } from "@/lib/queries";

interface Lane {
  waiting: number | null;
  in_flight: number | null;
  stale: number;
  consumers: number;
}
/** The pipeline endpoint returns a free-form aggregate; the fields we read. */
interface Pipeline {
  components: Record<string, string>;
  lanes: Record<string, Lane>;
  counts: Record<string, number>;
  qdrant_points: number | null;
  in_flight_parse: {
    doc_id?: string;
    title: string | null;
    idx: number;
    page_start: number;
    page_end: number;
    worker_id: string | null;
  }[];
}

const COMP_LABELS: Record<string, string> = {
  postgres: "postgres",
  redis: "redis",
  qdrant: "qdrant",
  tei_query: "tei-query",
  embed_backend: "embed (jetson)",
};

const STREAM_LABELS: Record<string, string> = {
  "doc.split": "doc.split",
  "doc.parse": "doc.parse",
  "doc.embed": "doc.embed",
};

function Dot({ state }: { state: string }) {
  return (
    <span
      className={cn(
        "inline-block size-2 rounded-full",
        state === "ok" ? "animate-pulse-dot bg-press" : "bg-redink",
      )}
    />
  );
}

function LaneRow({ name, lane }: { name: string; lane: Lane }) {
  return (
    <TableRow>
      <TableCell className="font-mono text-[12.5px]">{STREAM_LABELS[name] ?? name}</TableCell>
      <TableCell className="tabular-nums">{lane.waiting ?? "—"}</TableCell>
      <TableCell className="tabular-nums">{lane.in_flight ?? "—"}</TableCell>
      <TableCell
        className={cn("tabular-nums", lane.stale > 0 ? "text-warning" : "text-muted-foreground")}
      >
        {lane.stale}
      </TableCell>
      <TableCell className="tabular-nums">{lane.consumers}</TableCell>
    </TableRow>
  );
}

/** Visual queue lane between pipeline stages (mockup v2 style). */
function LaneBox({ name, lane }: { name: string; lane?: Lane }) {
  if (!lane) return <div className="flex min-w-[110px] flex-1 flex-col justify-center px-2.5" />;
  // Short labels — the lane box is narrow; pills must fit 2 lines max.
  const pills: { cls: string; label: string }[] = [];
  if (lane.in_flight && lane.in_flight > 0) {
    pills.push({ cls: "r", label: `${lane.in_flight} run` });
  }
  if (lane.stale > 0) {
    pills.push({ cls: "s", label: `${lane.stale} stale` });
  }
  if (lane.waiting) {
    pills.push({ cls: "w", label: `+${lane.waiting.toLocaleString()}` });
  }
  return (
    <div className="flex min-w-[110px] flex-1 flex-col justify-center px-2.5">
      <div className="mb-[5px] flex items-baseline justify-between">
        <span className="text-[11px] font-semibold text-muted-foreground">{name}</span>
        <span className="font-mono text-xs font-semibold text-warning tabular-nums">
          {lane.waiting?.toLocaleString() ?? "—"}
        </span>
      </div>
      <div className="flex min-h-10 flex-wrap items-center gap-1 rounded border border-sheet-edge bg-paper-deep px-2 py-1.5">
        {pills.map((p) => (
          <span
            key={p.cls}
            className={cn(
              "rounded-[2px] border px-2 py-0.5 font-mono text-[10.5px] font-medium whitespace-nowrap",
              p.cls === "w" && "border-ledger bg-ledger-wash text-ledger",
              p.cls === "r" && "animate-blink border-press bg-press-wash text-press-deep",
              p.cls === "s" && "border-warning bg-ochre-wash text-warning",
            )}
          >
            {p.label}
          </span>
        ))}
      </div>
      <div className="animate-wire mt-1.5 h-0.5 rounded-sm opacity-75 [background:repeating-linear-gradient(90deg,var(--ink-faint)_0_6px,transparent_6px_14px)] [background-size:20px_2px]" />
    </div>
  );
}

function Stage({
  no,
  title,
  big,
  sm,
}: {
  no: number;
  title: string;
  big: React.ReactNode;
  sm: React.ReactNode;
}) {
  return (
    <div className="w-43 shrink-0 rounded-lg border border-sheet-edge bg-sheet px-3.5 py-3 max-[900px]:w-full">
      <h4 className="mb-1.5 flex items-center gap-1.5 text-[11px] font-semibold tracking-[0.02em] text-muted-foreground">
        <span className="inline-flex size-4 items-center justify-center rounded-full bg-press font-mono text-[9.5px] font-bold text-rail">
          {no}
        </span>
        {title}
      </h4>
      <div className="font-mono text-[13.5px] font-semibold tabular-nums">{big}</div>
      <div className="mt-1 text-[11px] leading-normal text-muted-foreground/70">{sm}</div>
    </div>
  );
}

/** Stat numeral — the "open counter" treatment, not a boxed card. */
function Stat({ num, label }: { num: React.ReactNode; label: React.ReactNode }) {
  return (
    <div className="border-b border-sheet-edge pb-4 pt-1.5">
      <div className="font-mono text-2xl font-medium leading-[1.1] tracking-[-0.02em] tabular-nums">
        {num}
      </div>
      <div className="mt-1 text-[11px] text-muted-foreground">{label}</div>
    </div>
  );
}

export default function PipelinePage() {
  const { data: raw, error: queryError, isPending, isError } = usePipeline();
  const [history, setHistory] = useState<number[]>([]);
  const lastDone = useRef<number | null>(null);

  // The SDK types the aggregate loosely; narrow to the fields we read.
  const data = raw as unknown as Pipeline | null;
  // Background refetches can fail while stale data is still shown — TanStack
  // keeps `error` set, so capture it once as a plain string.
  const errMsg =
    queryError instanceof Error ? queryError.message : queryError ? String(queryError) : null;

  // Track shard throughput deltas between polls (same math as before).
  const done = data?.counts.shards_done ?? null;
  useEffect(() => {
    if (done === null) return;
    const prev = lastDone.current;
    if (prev !== null && done >= prev) {
      setHistory((h) => [...h.slice(-59), done - prev]);
    }
    lastDone.current = done;
  }, [done]);

  if (isPending) return <p className="text-muted-foreground">loading pipeline…</p>;
  if (isError || !data) {
    return <Card>pipeline API unreachable: {errMsg ?? "unknown error"}</Card>;
  }

  const c = data.counts;
  const samples = history.length;
  const rate = samples > 0 ? history.reduce((a, b) => a + b, 0) / (samples / 12) : null;
  const etaH =
    rate && rate > 0 && (c.shards_pending ?? 0) > 0
      ? ((c.shards_pending as number) / rate / 60).toFixed(1)
      : null;
  const readyPct = c.docs_total ? Math.round(((c.docs_ready ?? 0) / c.docs_total) * 100) : 0;
  const qdrantPoints = data.qdrant_points;

  return (
    <>
      <div className="mb-3 flex items-baseline justify-between">
        <h2 className="m-0 font-serif text-[21px] font-semibold tracking-[-0.005em]">
          Pipeline live view
        </h2>
        <span className="inline-flex items-center gap-1.5 rounded-full border border-sheet-edge bg-sheet px-3 py-1 text-xs font-medium text-muted-foreground">
          {errMsg ? (
            <>
              <span className="inline-block size-2 rounded-full bg-redink" /> retrying…
            </>
          ) : (
            <>
              <span className="animate-pulse-dot inline-block size-2 rounded-full bg-press" /> live
              · 5s
            </>
          )}
        </span>
      </div>

      <div className="grid grid-cols-2 gap-x-6 md:grid-cols-5">
        <Stat
          num={
            <>
              {c.docs_ready ?? "—"}
              <span className="text-[0.55em] text-muted-foreground">/{c.docs_total ?? "—"}</span>
            </>
          }
          label={<>docs ready · {readyPct}%</>}
        />
        <Stat
          num={c.shards_done ?? "—"}
          label={
            <>
              shards done · <span className="text-press">{c.shards_failed ?? 0} failed</span>
            </>
          }
        />
        <Stat num={c.shards_pending ?? "—"} label="pending shards" />
        <Stat
          num={
            rate !== null ? (
              <>
                {rate.toFixed(1)}
                <span className="text-[0.55em] text-muted-foreground">/min</span>
              </>
            ) : (
              <span className="text-muted-foreground">measuring…</span>
            )
          }
          label="live throughput"
        />
        <Stat
          num={etaH ? `~${etaH}h` : <span className="text-muted-foreground">—</span>}
          label="ETA at live rate"
        />
      </div>

      <Card className="mt-5">
        <CardHeader>
          <CardTitle>Components</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="flex flex-wrap gap-2">
            {Object.entries(data.components).map(([name, state]) => (
              <span
                key={name}
                className="inline-flex items-center gap-[7px] rounded border border-sheet-edge bg-sheet px-2.5 py-1 text-[12.5px] font-medium"
              >
                <Dot state={state} /> {COMP_LABELS[name] ?? name}
              </span>
            ))}
          </div>
        </CardContent>
      </Card>

      {/* ===== visual pipeline flow: stage → lane → stage ===== */}
      <Card className="mt-4">
        <CardHeader>
          <CardTitle>Flow</CardTitle>
        </CardHeader>
        <CardContent className="overflow-x-auto">
          <div className="flex items-stretch gap-0 max-[900px]:flex-col max-[900px]:gap-2">
            <Stage
              no={1}
              title="Split"
              big={`${c.docs_parsing + c.docs_ready} docs`}
              sm={<>splitter · nssp</>}
            />
            <LaneBox name="doc.split" lane={data.lanes["doc.split"]} />
            <Stage
              no={2}
              title={`Parse ×${data.lanes["doc.parse"]?.consumers ?? "—"}`}
              big={`${c.docs_parsing_active ?? "—"} active`}
              sm={
                <>
                  nssp×3 + nsschat×3
                  <br />
                  avg 124s/shard
                </>
              }
            />
            <LaneBox name="doc.parse" lane={data.lanes["doc.parse"]} />
            <Stage
              no={3}
              title="Embed"
              big={
                data.components.embed_backend === "ok"
                  ? `${c.docs_awaiting_embed ?? 0} queued`
                  : "embed down"
              }
              sm={
                <>
                  ollama bge-m3
                  <br />
                  whole-book barrier
                </>
              }
            />
            <LaneBox name="doc.embed" lane={data.lanes["doc.embed"]} />
            <Stage no={4} title="Index" big={`${qdrantPoints ?? "—"} pts`} sm="qdrant chunks" />
          </div>
        </CardContent>
      </Card>

      <Card className="mt-4">
        <CardHeader>
          <CardTitle>Queue lanes — waiting / in-flight / stale / consumers</CardTitle>
        </CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  stream
                </TableHead>
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  waiting
                </TableHead>
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  in-flight
                </TableHead>
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  stale &gt;15m
                </TableHead>
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  active consumers
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {Object.entries(data.lanes).map(([name, lane]) => (
                <LaneRow key={name} name={name} lane={lane} />
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <Card className="mt-4">
        <CardHeader>
          <CardTitle>In-flight parse jobs (lease holders)</CardTitle>
        </CardHeader>
        <CardContent>
          {data.in_flight_parse.length === 0 ? (
            <p className="m-0 text-muted-foreground">no shard currently claimed</p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                    doc
                  </TableHead>
                  <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                    shard
                  </TableHead>
                  <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                    pages
                  </TableHead>
                  <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                    worker
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.in_flight_parse.map((j) => (
                  <TableRow key={`${j.doc_id ?? j.title}-${j.idx}`}>
                    <TableCell>{j.title ?? "(untitled)"}</TableCell>
                    <TableCell className="tabular-nums">#{j.idx}</TableCell>
                    <TableCell className="font-mono text-[12.5px] tabular-nums">
                      {j.page_start}–{j.page_end}
                    </TableCell>
                    <TableCell className="font-mono text-[12.5px] text-muted-foreground">
                      {j.worker_id ?? "—"}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {errMsg && (
        <p className="mt-2 mb-0 text-xs text-muted-foreground">
          last refresh failed: {errMsg} — retrying in 5s
        </p>
      )}
    </>
  );
}
