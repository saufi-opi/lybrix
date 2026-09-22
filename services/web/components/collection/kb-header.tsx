"use client";

/** KB detail header — WeKnora's knowledge-base banner: name, stat badges
 * (docs, chunks, byte_size, bound model), the Quick Ingest dropdown (the
 * shared 3-tab hub), and a settings gear that jumps to the Settings tab. */

import { Database, FileText, HardDrive, Layers, Plus, Settings2 } from "lucide-react";
import { useCallback, useState } from "react";
import { IngestDialog } from "@/components/ingest/ingest-dialog";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Skeleton } from "@/components/ui/skeleton";
import type { CollectionStats } from "@/lib/api-client";

function formatBytes(n: number): string {
  if (n >= 1024 * 1024 * 1024) return `${(n / (1024 * 1024 * 1024)).toFixed(1)} GB`;
  if (n >= 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MB`;
  if (n >= 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${n} B`;
}

function StatBadge({
  icon,
  label,
  value,
}: {
  icon: React.ReactNode;
  label: string;
  value: React.ReactNode;
}) {
  return (
    <span className="inline-flex items-center gap-1.5 rounded border border-sheet-edge bg-paper-deep px-2.5 py-1 text-[12px] font-medium">
      {icon}
      <span className="text-muted-foreground">{label}</span>
      <span className="font-mono tabular-nums">{value}</span>
    </span>
  );
}

export function KbHeader({
  id,
  name,
  stats,
  onSettings,
}: {
  id: string;
  name: string;
  stats: CollectionStats | undefined;
  onSettings: () => void;
}) {
  const [ingestOpen, setIngestOpen] = useState(false);
  const [ingestSource, setIngestSource] = useState<"local" | "url" | "opds">("local");
  const [doneTick, setDoneTick] = useState(0);
  const onDone = useCallback(() => setDoneTick((t) => t + 1), []);

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex min-w-0 flex-col gap-1">
          <h2 className="m-0 flex items-center gap-2 font-serif text-[21px] font-semibold tracking-[-0.005em]">
            <Database className="size-5 shrink-0 text-press" aria-hidden="true" />
            <span className="min-w-0 truncate">{name}</span>
          </h2>
          <p className="m-0 font-mono text-[11.5px] text-muted-foreground">{id}</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {/* Quick Ingest dropdown — WeKnora's KB-level multi-source entry */}
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button type="button" size="sm">
                <Plus aria-hidden="true" />
                Quick ingest
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end">
              <DropdownMenuItem
                onSelect={() => {
                  setIngestSource("local");
                  setIngestOpen(true);
                }}
              >
                Upload local files
              </DropdownMenuItem>
              <DropdownMenuItem
                onSelect={() => {
                  setIngestSource("url");
                  setIngestOpen(true);
                }}
              >
                Import via URL
              </DropdownMenuItem>
              <DropdownMenuItem
                onSelect={() => {
                  setIngestSource("opds");
                  setIngestOpen(true);
                }}
              >
                Calibre-Web OPDS sync
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
          <Button type="button" variant="outline" size="sm" onClick={onSettings}>
            <Settings2 aria-hidden="true" />
            Settings
          </Button>
        </div>
      </div>

      <div className="flex flex-wrap gap-2">
        {stats ? (
          <>
            <StatBadge
              icon={<FileText className="size-3.5 text-ledger" aria-hidden="true" />}
              label="docs"
              value={stats.doc_count}
            />
            <StatBadge
              icon={<Layers className="size-3.5 text-ledger" aria-hidden="true" />}
              label="chunks"
              value={stats.chunk_count}
            />
            <StatBadge
              icon={<HardDrive className="size-3.5 text-ledger" aria-hidden="true" />}
              label="size"
              value={formatBytes(stats.byte_size)}
            />
            <StatBadge
              icon={<Database className="size-3.5 text-ledger" aria-hidden="true" />}
              label="model"
              value={stats.embedding_model}
            />
          </>
        ) : (
          <>
            <Skeleton className="h-7 w-20" />
            <Skeleton className="h-7 w-24" />
            <Skeleton className="h-7 w-24" />
            <Skeleton className="h-7 w-36" />
          </>
        )}
      </div>

      {/* shared 3-tab hub, pre-bound to this KB; key on doneTick + source so
          each open mounts fresh state in the chosen tab's default */}
      <IngestDialog
        key={`${ingestSource}-${doneTick}`}
        open={ingestOpen}
        onOpenChange={setIngestOpen}
        collectionId={id}
        onDone={onDone}
      />
    </div>
  );
}
