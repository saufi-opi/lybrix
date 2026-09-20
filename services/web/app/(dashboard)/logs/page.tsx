"use client";

/** Logs (PRD §8.1 screen 5): unified filterable event stream. Polls every 5s
 * via useEvents(); mono rows — the ledger look. */

import { useSearchParams } from "next/navigation";
import { Suspense } from "react";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useEvents } from "@/lib/queries";

function EventsTable() {
  const params = useSearchParams();
  const docId = params.get("doc_id") ?? "";
  const level = params.get("level") ?? "";

  // Same query contract as the old server version: ?level=&doc_id=
  const sp = new URLSearchParams();
  if (level) sp.set("level", level);
  if (docId) sp.set("doc_id", docId);
  const { data: events, isPending, isError, error } = useEvents(`?${sp.toString()}`);

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-serif text-[19px] font-semibold">Events</CardTitle>
      </CardHeader>
      <CardContent>
        {isPending ? (
          <p className="m-0 text-muted-foreground">loading events…</p>
        ) : isError ? (
          <p className="m-0 text-redink">events API unreachable: {error.message}</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  time
                </TableHead>
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  level
                </TableHead>
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  stage
                </TableHead>
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  code
                </TableHead>
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  message
                </TableHead>
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  document
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {events.map((e) => (
                <TableRow key={String(e.id)}>
                  <TableCell className="text-muted-foreground font-mono text-[12px] whitespace-nowrap">
                    {new Date(String(e.created_at)).toLocaleString()}
                  </TableCell>
                  <TableCell className="font-mono text-[12.5px]">{String(e.level)}</TableCell>
                  <TableCell className="font-mono text-[12.5px]">
                    {String(e.stage ?? "—")}
                  </TableCell>
                  <TableCell className="font-mono text-[12.5px]">{String(e.code ?? "—")}</TableCell>
                  <TableCell className="min-w-60 whitespace-normal">{String(e.message)}</TableCell>
                  <TableCell>
                    {e.doc_id ? (
                      <a
                        href={`/documents/${String(e.doc_id)}`}
                        className="text-ledger no-underline hover:underline"
                      >
                        open
                      </a>
                    ) : (
                      "—"
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}

/** The old page filtered server-side via ?level=&stage=&doc_id= params; the
 * client version keeps the same query contract via useSearchParams + useEvents. */
function LogsInner() {
  return <EventsTable />;
}

export default function LogsPage() {
  return (
    <Suspense fallback={<p className="text-muted-foreground">loading…</p>}>
      <LogsInner />
    </Suspense>
  );
}
