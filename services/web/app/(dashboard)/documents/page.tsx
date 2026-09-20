"use client";

/** Documents list (PRD §8.1 screen 2): filters, progress, state badges.
 * Client component — useDocuments() refetches on window focus, so filter
 * changes and retry results refresh without a full page navigation. */

import { useRouter, useSearchParams } from "next/navigation";
import { Suspense } from "react";
import { ProgressBar } from "@/components/progress-bar";
import { StateBadge } from "@/components/state-badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useDocuments } from "@/lib/queries";

const STATES = [
  "uploaded",
  "splitting",
  "parsing",
  "embedding",
  "ready",
  "partial",
  "failed",
  "archived",
] as const;

function DocumentsList() {
  const router = useRouter();
  const params = useSearchParams();
  const state = params.get("state") ?? "";
  const q = params.get("q") ?? "";

  function setFilter(key: "state" | "q", value: string) {
    const sp = new URLSearchParams(params.toString());
    if (value) sp.set(key, value);
    else sp.delete(key);
    router.replace(`/documents?${sp.toString()}`, { scroll: false });
  }

  // Keep the same query params as the old server version: ?state=&q=
  const sp = new URLSearchParams();
  if (state) sp.set("state", state);
  if (q) sp.set("q", q);
  const { data: docs, isPending, isError, error } = useDocuments(`?${sp.toString()}`);

  return (
    <>
      <Card size="sm" className="mb-4">
        <CardContent className="flex flex-wrap items-center gap-2 py-2">
          <Input
            name="q"
            placeholder="Search title…"
            defaultValue={q}
            className="w-56"
            onKeyDown={(e) => {
              if (e.key === "Enter") setFilter("q", (e.target as HTMLInputElement).value);
            }}
            onBlur={(e) => setFilter("q", e.target.value)}
          />
          <Select
            value={state || "any"}
            onValueChange={(v) => setFilter("state", v === "any" ? "" : v)}
          >
            <SelectTrigger className="w-40" aria-label="filter by state">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="any">any state</SelectItem>
              {STATES.map((s) => (
                <SelectItem key={s} value={s}>
                  {s}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Documents</CardTitle>
        </CardHeader>
        <CardContent>
          {isPending ? (
            <p className="m-0 text-muted-foreground">loading documents…</p>
          ) : isError ? (
            <p className="m-0 text-redink">documents API unreachable: {error.message}</p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                    title
                  </TableHead>
                  <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                    pages
                  </TableHead>
                  <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                    state
                  </TableHead>
                  <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                    progress
                  </TableHead>
                  <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                    completeness
                  </TableHead>
                  <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                    updated
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {docs.map((d) => {
                  const pct =
                    d.total_shards && d.total_shards > 0
                      ? Math.round(((d.shards_done + d.shards_failed) / d.total_shards) * 75)
                      : 0;
                  return (
                    <TableRow key={d.id}>
                      <TableCell className="font-medium">
                        <a
                          href={`/documents/${d.id}`}
                          className="text-ledger no-underline hover:underline"
                        >
                          {d.title ?? d.id}
                        </a>
                      </TableCell>
                      <TableCell className="tabular-nums">{d.page_count ?? "—"}</TableCell>
                      <TableCell>
                        <StateBadge state={d.state} />
                      </TableCell>
                      <TableCell className="min-w-30">
                        <ProgressBar
                          pct={pct}
                          label={`${d.shards_done}/${d.total_shards ?? "?"} shards`}
                        />
                      </TableCell>
                      <TableCell className="tabular-nums">
                        {d.completeness != null ? `${Math.round(d.completeness * 100)}%` : "—"}
                      </TableCell>
                      <TableCell className="text-muted-foreground tabular-nums">
                        {new Date(d.updated_at).toLocaleString()}
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {/* Retry actions live on the detail page; the documents list stays
       * read-only, exactly as before. */}
    </>
  );
}

export default function DocumentsPage() {
  return (
    <Suspense fallback={<p className="text-muted-foreground">loading…</p>}>
      <DocumentsList />
    </Suspense>
  );
}
