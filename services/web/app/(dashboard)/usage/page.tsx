"use client";

/** Usage (Phase 2): summary cards + per-key table, aggregated from key_usage
 * via GET /api/admin/v1/usage/summary. Period switcher: 24h / 7d / 30d —
 * shadcn Tabs. */

import { cn } from "cn";
import { useCallback, useEffect, useState } from "react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";

interface KeyUsageRow {
  key_id: string;
  name: string | null;
  calls: number;
  last_used_at: string | null;
}
interface Summary {
  period: string;
  total: number;
  by_key: KeyUsageRow[];
}

type Period = "24h" | "7d" | "30d";
const PERIODS: Period[] = ["24h", "7d", "30d"];

/** Stat numeral — the "open counter" treatment, not a boxed card. */
function Stat({
  num,
  label,
  className,
}: {
  num: React.ReactNode;
  label: string;
  className?: string;
}) {
  return (
    <div className="border-b border-sheet-edge pb-4 pt-1.5">
      <div
        className={cn(
          "font-mono text-[1.7rem] font-medium tracking-[-0.02em] tabular-nums",
          className,
        )}
      >
        {num}
      </div>
      <p className="mt-1 mb-0 text-xs text-muted-foreground">{label}</p>
    </div>
  );
}

export default function UsagePage() {
  const [period, setPeriod] = useState<Period>("24h");
  const [current, setCurrent] = useState<Summary | null>(null);
  const [week, setWeek] = useState<Summary | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  const load = useCallback(async (p: Period) => {
    setLoading(true);
    try {
      const [c, w] = await Promise.all([
        fetch(`/api/admin/v1/usage/summary?period=${p}`).then((r) => {
          if (!r.ok) throw new Error(`API ${r.status}`);
          return r.json() as Promise<Summary>;
        }),
        fetch("/api/admin/v1/usage/summary?period=7d").then((r) =>
          r.ok ? (r.json() as Promise<Summary>) : null,
        ),
      ]);
      setCurrent(c);
      setWeek(w);
      setError(null);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load(period);
  }, [period, load]);

  const top = current?.by_key[0];
  const activeKeys = current?.by_key.filter((k) => k.calls > 0).length ?? 0;

  return (
    <Card>
      <CardHeader>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <CardTitle className="font-serif text-[19px] font-semibold">Usage</CardTitle>
          <Tabs value={period} onValueChange={(v) => setPeriod(v as Period)}>
            <TabsList>
              {PERIODS.map((p) => (
                <TabsTrigger key={p} value={p}>
                  {p}
                </TabsTrigger>
              ))}
            </TabsList>
          </Tabs>
        </div>
      </CardHeader>
      <CardContent>
        {error && (
          <p className="mt-0 mb-3 font-mono text-[12.5px] text-redink" role="alert">
            {error}
          </p>
        )}
        {loading && !current ? (
          <p className="m-0 text-muted-foreground">Loading usage…</p>
        ) : (
          <>
            <div className="grid grid-cols-2 gap-x-6 md:grid-cols-4">
              <Stat num={current?.total ?? "—"} label={`calls · ${period}`} />
              <Stat num={week?.total ?? "—"} label="calls · 7d" />
              <Stat num={activeKeys} label={`keys used · ${period}`} />
              <Stat
                num={top ? (top.name ?? top.key_id.slice(0, 8)) : "—"}
                label={top ? `top key · ${top.calls} calls` : "top key"}
                className="text-[0.95rem] font-semibold break-word text-press-deep"
              />
            </div>
            {current && current.by_key.length > 0 && (
              <div className="mt-4">
                <Table>
                  <TableHeader>
                    <TableRow className="hover:bg-transparent">
                      <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                        key
                      </TableHead>
                      <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                        calls
                      </TableHead>
                      <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                        last used
                      </TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {current.by_key.map((k) => (
                      <TableRow key={k.key_id}>
                        <TableCell>
                          {k.name ?? <code className="font-mono">{k.key_id.slice(0, 8)}</code>}
                        </TableCell>
                        <TableCell className="tabular-nums">{k.calls}</TableCell>
                        <TableCell className="text-muted-foreground tabular-nums">
                          {k.last_used_at ? new Date(k.last_used_at).toLocaleString() : "—"}
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            )}
            {current && current.by_key.length === 0 && (
              <p className="m-0 text-muted-foreground">
                No authenticated calls in the last {period}.
              </p>
            )}
          </>
        )}
      </CardContent>
    </Card>
  );
}
