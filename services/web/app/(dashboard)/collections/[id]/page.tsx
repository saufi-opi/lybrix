"use client";

/** Knowledge Base detail workspace — WeKnora's KB-centric surface with 4
 * tabs: Documents, Chunk Inspector, Search Test, Settings. Client shell;
 * /collections/[id] keeps the flat-URL convention (decision 1). */

import { useParams } from "next/navigation";
import { useCallback, useState } from "react";
import { ChunkInspectorTab } from "@/components/collection/chunk-inspector-tab";
import { DocumentsTab } from "@/components/collection/documents-tab";
import { KbHeader } from "@/components/collection/kb-header";
import { SearchTestTab } from "@/components/collection/search-test-tab";
import { SettingsTab } from "@/components/collection/settings-tab";
import { Card, CardContent } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useCollectionStats, useCollections, useDocuments } from "@/lib/queries";

export default function CollectionDetailPage() {
  const params = useParams<{ id: string }>();
  const id = params.id;

  const cols = useCollections();
  const stats = useCollectionStats(id);
  // wide fetch for the chunk tab's doc picker (statuses flip on focus refetch)
  const docs = useDocuments(`?collection=${encodeURIComponent(id)}&limit=200`);

  const [tab, setTab] = useState("documents");
  const jumpToSettings = useCallback(() => setTab("settings"), []);

  const name =
    ((cols.data ?? []) as Array<{ id: string; name: string }>).find((c) => c.id === id)?.name ?? id;

  if (cols.isPending) {
    return <p className="text-muted-foreground">loading knowledge base…</p>;
  }
  if (cols.isError) {
    return <p className="text-redink">collections API unreachable: {cols.error.message}</p>;
  }

  return (
    <div className="flex flex-col gap-4">
      <KbHeader id={id} name={name} stats={stats.data} onSettings={jumpToSettings} />

      <Tabs value={tab} onValueChange={setTab}>
        <TabsList>
          <TabsTrigger value="documents">Documents</TabsTrigger>
          <TabsTrigger value="chunks">Chunk Inspector</TabsTrigger>
          <TabsTrigger value="search">Search Test</TabsTrigger>
          <TabsTrigger value="settings">Settings</TabsTrigger>
        </TabsList>

        <TabsContent value="documents" className="mt-4">
          <DocumentsTab collectionId={id} />
        </TabsContent>

        <TabsContent value="chunks" className="mt-4">
          {docs.isPending ? (
            <Skeleton className="h-40 w-full" />
          ) : docs.isError ? (
            <Card>
              <CardContent>
                <p className="text-redink">documents API unreachable: {docs.error.message}</p>
              </CardContent>
            </Card>
          ) : (
            <ChunkInspectorTab collectionId={id} documents={docs.data ?? []} />
          )}
        </TabsContent>

        <TabsContent value="search" className="mt-4">
          <SearchTestTab collectionId={id} />
        </TabsContent>

        <TabsContent value="settings" className="mt-4">
          <SettingsTab collectionId={id} stats={stats.data} />
        </TabsContent>
      </Tabs>
    </div>
  );
}
