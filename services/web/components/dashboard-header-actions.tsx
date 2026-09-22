"use client";

/** Dashboard header actions — the standalone ingest hub entry point. Upload
 * left the dual-zone rail (it became an action inside Knowledge Bases); this
 * keeps /upload one click away from every page header. Hidden on the mobile
 * top-strip keeps the header row from crowding. */

import { Upload } from "lucide-react";
import Link from "next/link";
import { Button } from "@/components/ui/button";

export function DashboardHeaderActions() {
  return (
    <Button type="button" variant="outline" size="sm" asChild className="max-[900px]:hidden">
      <Link href="/upload">
        <Upload aria-hidden="true" />
        Ingest documents
      </Link>
    </Button>
  );
}
