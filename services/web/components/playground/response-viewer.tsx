"use client";

/** Playground response viewer: Rendered tab (tool result rendered per shape
 * — search hits as chunk cards with page citations, read_pages/preview_chunk
 * as formatted markdown, get_document as a metadata card) and Raw JSON tab
 * (pretty-printed mono). Every result carries the citation triple (PRD §7.2),
 * so the rendered search treatment shows doc_title + page range prominently. */

import { Markdown } from "@/components/markdown";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { prepareChunkMarkdown } from "@/lib/markdown";

interface CallResponse {
  result: unknown;
  isError: boolean;
  latencyMs: number;
}

/** MCP tool results wrap content in { content: [{type, text}], isError }. */
function unwrap(result: unknown): unknown {
  if (result !== null && typeof result === "object" && "content" in (result as object)) {
    const content = (result as { content?: { type?: string; text?: string }[] }).content;
    if (Array.isArray(content) && content.length > 0 && content[0]?.type === "text") {
      try {
        return JSON.parse(content[0]?.text ?? "");
      } catch {
        return content[0]?.text;
      }
    }
  }
  return result;
}

export function ResponseViewer({ toolName, resp }: { toolName: string; resp: CallResponse }) {
  const unwrapped = unwrap(resp.result);

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 font-mono text-[14px]">
          response
          {resp.isError && <Badge variant="destructive">error</Badge>}
        </CardTitle>
      </CardHeader>
      <CardContent>
        <Tabs defaultValue="rendered">
          <TabsList>
            <TabsTrigger value="rendered">Rendered</TabsTrigger>
            <TabsTrigger value="raw">Raw JSON</TabsTrigger>
          </TabsList>
          <TabsContent value="rendered" className="mt-3">
            <Rendered toolName={toolName} value={unwrapped} isError={resp.isError} />
          </TabsContent>
          <TabsContent value="raw" className="mt-3">
            <pre className="max-h-[420px] overflow-auto rounded-lg border border-sheet-edge bg-rail p-3 font-mono text-xs leading-normal whitespace-pre-wrap">
              {JSON.stringify(resp.result, null, 2)}
            </pre>
          </TabsContent>
        </Tabs>
      </CardContent>
    </Card>
  );
}

function Rendered({
  toolName,
  value,
  isError,
}: {
  toolName: string;
  value: unknown;
  isError: boolean;
}) {
  if (isError) {
    return (
      <p className="m-0 font-mono text-[12.5px] text-redink whitespace-pre-wrap">
        {JSON.stringify(value, null, 2)}
      </p>
    );
  }

  if (toolName === "search" && Array.isArray(value)) {
    const hits = value as {
      doc_title?: string;
      doc_id?: string;
      page_start?: number;
      page_end?: number;
      heading_path?: string[] | null;
      text?: string;
      score?: number;
      partial?: boolean;
      note?: string;
    }[];
    if (hits.length === 0) return <p className="m-0 text-muted-foreground">no hits</p>;
    return (
      <div className="flex flex-col gap-2">
        {hits.map((h, i) => (
          <div
            // biome-ignore lint/suspicious/noArrayIndexKey: hits have no stable id
            key={i}
            className="rounded-lg border border-sheet-edge bg-sheet px-3.5 py-2.5"
          >
            <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1">
              <span className="text-[13px] font-semibold">
                {h.doc_title ?? h.doc_id ?? "—"}
                {typeof h.page_start === "number" && (
                  <span className="font-mono text-xs font-normal text-muted-foreground">
                    {" "}
                    · pp. {h.page_start}
                    {typeof h.page_end === "number" && h.page_end !== h.page_start
                      ? `–${h.page_end}`
                      : ""}
                  </span>
                )}
              </span>
              <span className="font-mono text-xs text-press tabular-nums">
                {typeof h.score === "number" ? h.score.toFixed(4) : "—"}
              </span>
            </div>
            {h.heading_path && h.heading_path.length > 0 && (
              <span className="mt-0.5 block font-mono text-[11px] text-muted-foreground">
                {h.heading_path.join(" › ")}
              </span>
            )}
            {typeof h.text === "string" && h.text && (
              <Markdown className="mt-1.5">{prepareChunkMarkdown(h.text)}</Markdown>
            )}
            {h.partial && h.note && (
              <p className="mt-1.5 mb-0 text-[11px] text-warning">△ {h.note}</p>
            )}
          </div>
        ))}
      </div>
    );
  }

  if (toolName === "read_pages" && value !== null && typeof value === "object") {
    const r = value as {
      doc_title?: string;
      page_start?: number;
      page_end?: number;
      markdown?: string;
      truncated_to?: number;
    };
    return (
      <div className="flex flex-col gap-2">
        <div className="flex items-baseline gap-2">
          <span className="text-[13px] font-semibold">{r.doc_title ?? "—"}</span>
          <span className="font-mono text-xs text-muted-foreground">
            pp. {r.page_start}–{r.page_end}
            {r.truncated_to ? ` (cap ${r.truncated_to})` : ""}
          </span>
        </div>
        <Markdown className="max-h-[420px] overflow-auto rounded-lg border border-sheet-edge bg-sheet p-3">
          {prepareChunkMarkdown(r.markdown ?? "")}
        </Markdown>
      </div>
    );
  }

  if (toolName === "get_document" && value !== null && typeof value === "object") {
    const d = value as {
      title?: string | null;
      author?: string | null;
      state?: string;
      page_count?: number | null;
      completeness?: number | null;
      chunk_count?: number;
    };
    return (
      <div className="flex flex-col gap-1.5 text-[13px]">
        <span className="font-serif text-[16px] font-semibold">{d.title ?? "(untitled)"}</span>
        {d.author && <span className="text-muted-foreground">{d.author}</span>}
        <div className="flex flex-wrap gap-1.5 pt-1">
          {d.state && <Badge variant="outline">{d.state}</Badge>}
          {typeof d.page_count === "number" && (
            <Badge variant="outline" className="font-mono">
              {d.page_count} pages
            </Badge>
          )}
          {typeof d.completeness === "number" && (
            <Badge variant="outline" className="font-mono">
              {Math.round(d.completeness * 100)}% complete
            </Badge>
          )}
          {typeof d.chunk_count === "number" && (
            <Badge variant="outline" className="font-mono">
              {d.chunk_count} chunks
            </Badge>
          )}
        </div>
      </div>
    );
  }

  if (toolName === "preview_chunk" && value !== null && typeof value === "object") {
    const c = value as {
      chunk_id?: string;
      seq?: number;
      is_parent?: boolean;
      page_start?: number | null;
      page_end?: number | null;
      heading_path?: string[] | null;
      token_count?: number;
      text?: string;
      parent_text?: string;
    };
    const breadcrumb = (c.heading_path ?? []).join(" › ");
    return (
      <div className="flex flex-col gap-2">
        <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-0.5">
          <span className="font-mono text-xs text-muted-foreground">seq {c.seq ?? "—"}</span>
          {typeof c.page_start === "number" && (
            <span className="font-mono text-xs text-muted-foreground">
              pp. {c.page_start}
              {typeof c.page_end === "number" && c.page_end !== c.page_start
                ? `–${c.page_end}`
                : ""}
            </span>
          )}
          {c.is_parent && <Badge variant="outline">parent</Badge>}
          {typeof c.token_count === "number" && (
            <span className="font-mono text-xs text-muted-foreground">{c.token_count} tokens</span>
          )}
        </div>
        {breadcrumb && (
          <span className="font-mono text-[11px] text-muted-foreground">{breadcrumb}</span>
        )}
        <Markdown className="max-h-[420px] overflow-auto rounded-lg border border-sheet-edge bg-sheet p-3">
          {prepareChunkMarkdown(c.text ?? "")}
        </Markdown>
        {c.parent_text && (
          <div className="flex flex-col gap-1">
            <span className="font-mono text-[11px] text-muted-foreground">parent context</span>
            <Markdown className="max-h-[240px] overflow-auto rounded-lg border border-sheet-edge bg-paper-deep/40 p-3">
              {prepareChunkMarkdown(c.parent_text)}
            </Markdown>
          </div>
        )}
      </div>
    );
  }

  // list_collections / list_documents / get_chunk_context / anything else:
  // pretty-printed mono (structured, bounded — no need for bespoke views).
  return (
    <pre className="max-h-[420px] overflow-auto rounded-lg border border-sheet-edge bg-rail p-3 font-mono text-xs leading-normal whitespace-pre-wrap">
      {JSON.stringify(value, null, 2)}
    </pre>
  );
}
