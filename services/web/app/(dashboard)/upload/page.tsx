"use client";

/** Upload hub (2.0.1 → WeKnora revamp): the shared 3-tab ingest content
 * rendered full-page (no dialog chrome). Logic lives in
 * components/ingest/ingest-dialog.tsx so the KB detail quick-ingest dialog
 * shares it (Phase 4). Feedback via sonner toasts + itemized status lines.
 * OPDS credentials are session-only — nothing persists. */

import { IngestContent, useIngestState } from "@/components/ingest/ingest-dialog";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";

export default function UploadPage() {
  const ingest = useIngestState();
  return (
    <Card>
      <CardHeader>
        <CardTitle className="font-serif text-[19px] font-semibold">Ingest sources</CardTitle>
        <CardDescription>
          Local files upload via presigned URLs — the API never buffers bytes. Remote URLs and OPDS
          feeds are fetched server-side into MinIO.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <IngestContent ingest={ingest} />
      </CardContent>
    </Card>
  );
}
