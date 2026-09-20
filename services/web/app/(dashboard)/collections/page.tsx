"use client";

/** Collections (PRD §8.1 screen 6): embedding model + dimension per collection.
 * Client component — useCollections() refetches on window focus. */

import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useCollections } from "@/lib/queries";

export default function CollectionsPage() {
  const { data: cols, isPending, isError, error } = useCollections();

  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-serif text-[19px] font-semibold">Collections</CardTitle>
        <CardDescription>
          Embedding model and dimension are immutable at creation — changing a model means a new
          collection and a re-embed migration (§8.1).
        </CardDescription>
      </CardHeader>
      <CardContent>
        {isPending ? (
          <p className="m-0 text-muted-foreground">loading collections…</p>
        ) : isError ? (
          <p className="m-0 text-redink">collections API unreachable: {error.message}</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow className="hover:bg-transparent">
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  id
                </TableHead>
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  name
                </TableHead>
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  model
                </TableHead>
                <TableHead className="text-[11.5px] tracking-[0.02em] text-muted-foreground">
                  docs
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {cols.map((c) => (
                <TableRow key={String(c.id)}>
                  <TableCell className="font-mono text-[12.5px]">{String(c.id)}</TableCell>
                  <TableCell className="font-medium">{String(c.name)}</TableCell>
                  <TableCell className="font-mono text-[12.5px]">
                    {String(c.embedding_model)}
                  </TableCell>
                  <TableCell className="tabular-nums">{String(c.doc_count ?? "—")}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}
