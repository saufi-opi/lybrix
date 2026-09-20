"use client";

/** MCP Playground (PRD §7.2, ADR 0003): exercise the six MCP tools from the
 * dashboard, protocol-true — calls go through /api/playground to the real
 * MCP server, so tool descriptions, top_k clamps and collection scoping are
 * exactly what a client sees. A scratch surface, not the eval harness
 * (scripts/eval). */

import { useCallback, useEffect, useState } from "react";
import { ResponseViewer } from "@/components/playground/response-viewer";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { useCollections } from "@/lib/queries";

interface McpTool {
  name: string;
  description?: string;
  inputSchema?: {
    type: string;
    properties?: Record<string, unknown>;
    required?: string[];
  };
}

interface CallResponse {
  result: unknown;
  isError: boolean;
  latencyMs: number;
}

export default function PlaygroundPage() {
  const { data: cols } = useCollections();
  const [tools, setTools] = useState<McpTool[] | null>(null);
  const [toolsError, setToolsError] = useState<string | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [args, setArgs] = useState<Record<string, unknown>>({});
  const [busy, setBusy] = useState(false);
  const [resp, setResp] = useState<CallResponse | null>(null);
  const [callError, setCallError] = useState<string | null>(null);

  useEffect(() => {
    fetch("/api/playground")
      .then(async (r) => {
        if (!r.ok) throw new Error(`API ${r.status}: ${(await r.text()).slice(0, 200)}`);
        return (await r.json()) as { tools: McpTool[] };
      })
      .then((b) => {
        setTools(b.tools);
        setToolsError(null);
      })
      .catch((e) => setToolsError(e instanceof Error ? e.message : String(e)));
  }, []);

  const tool = tools?.find((t) => t.name === selected) ?? null;

  const pick = useCallback((t: McpTool) => {
    setSelected(t.name);
    setArgs({});
    setResp(null);
    setCallError(null);
  }, []);

  const run = useCallback(async () => {
    if (!selected) return;
    setBusy(true);
    setCallError(null);
    try {
      const r = await fetch("/api/playground", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ tool: selected, args }),
      });
      if (!r.ok) throw new Error(`API ${r.status}: ${(await r.text()).slice(0, 200)}`);
      setResp((await r.json()) as CallResponse);
    } catch (e) {
      setCallError(e instanceof Error ? e.message : String(e));
      setResp(null);
    } finally {
      setBusy(false);
    }
  }, [selected, args]);

  return (
    <div className="flex flex-col gap-4">
      <Card>
        <CardHeader>
          <CardTitle className="font-serif text-[19px] font-semibold">MCP Playground</CardTitle>
          <CardDescription>
            Protocol-true calls to the real MCP server (<code className="font-mono">/mcp</code>) —
            tool descriptions, top_k clamps and collection scoping are exactly what a client sees.
            The server-side proxy holds the MCP key; the browser never does.
          </CardDescription>
        </CardHeader>
      </Card>

      {toolsError && (
        <p className="m-0 font-mono text-[12.5px] text-redink" role="alert">
          {toolsError}
        </p>
      )}

      <div className="grid gap-4 lg:grid-cols-[280px_1fr]">
        <div className="flex flex-col gap-2">
          {tools === null && !toolsError && (
            <div className="flex flex-col gap-2">
              <Skeleton className="h-14 w-full" />
              <Skeleton className="h-14 w-full" />
              <Skeleton className="h-14 w-full" />
            </div>
          )}
          {(tools ?? []).map((t) => (
            <button
              type="button"
              key={t.name}
              onClick={() => pick(t)}
              className={`rounded-lg border px-3 py-2 text-left transition-colors ${
                selected === t.name
                  ? "border-press bg-press-wash"
                  : "border-sheet-edge bg-sheet hover:bg-paper-deep"
              }`}
            >
              <span className="block font-mono text-[13px] font-semibold">{t.name}</span>
              {t.description && (
                <span className="mt-0.5 line-clamp-2 block text-xs text-muted-foreground">
                  {t.description}
                </span>
              )}
            </button>
          ))}
        </div>

        <div className="flex flex-col gap-4">
          {tool ? (
            <Card>
              <CardHeader>
                <CardTitle className="font-mono text-[14px]">{tool.name}</CardTitle>
                {tool.description && (
                  <CardDescription className="leading-snug">{tool.description}</CardDescription>
                )}
              </CardHeader>
              <CardContent className="flex flex-col gap-3">
                <ToolForm
                  tool={tool}
                  args={args}
                  setArgs={setArgs}
                  collections={(cols ?? []).map((c) => ({
                    id: String(c.id),
                    name: String(c.name),
                  }))}
                />
                <div className="flex items-center gap-3">
                  <Button type="button" onClick={() => void run()} disabled={busy}>
                    {busy ? "Calling…" : "Run tool"}
                  </Button>
                  {resp && (
                    <Badge variant="outline" className="font-mono tabular-nums">
                      {resp.latencyMs} ms
                    </Badge>
                  )}
                </div>

                {callError && (
                  <p className="m-0 font-mono text-[12.5px] text-redink" role="alert">
                    {callError}
                  </p>
                )}
              </CardContent>
            </Card>
          ) : (
            !toolsError && (
              <p className="m-0 text-muted-foreground">Pick a tool from the list to begin.</p>
            )
          )}

          {resp && tool && <ResponseViewer toolName={tool.name} resp={resp} />}
        </div>
      </div>
    </div>
  );
}

/** Renders inputs from the tool's inputSchema; smart prefills for collection
 * and doc_id (suggestions via the control-plane API — raw values still OK). */
function ToolForm({
  tool,
  args,
  setArgs,
  collections,
}: {
  tool: McpTool;
  args: Record<string, unknown>;
  setArgs: (a: Record<string, unknown>) => void;
  collections: { id: string; name: string }[];
}) {
  const props = tool.inputSchema?.properties ?? {};
  const required = new Set(tool.inputSchema?.required ?? []);
  const entries = Object.entries(props);

  if (entries.length === 0) {
    return <p className="m-0 text-xs text-muted-foreground">This tool takes no arguments.</p>;
  }

  return (
    <div className="flex flex-col gap-3">
      {entries.map(([key, rawSchema]) => {
        const schema = rawSchema as {
          type?: string;
          description?: string;
          enum?: string[];
          default?: unknown;
        };
        const value = args[key];
        const setValue = (v: unknown) => setArgs({ ...args, [key]: v });
        const isRequired = required.has(key);

        return (
          <div key={key} className="flex flex-col gap-1.5">
            <Label htmlFor={`arg-${key}`} className="text-xs font-semibold text-muted-foreground">
              {key}
              {isRequired ? "" : " (optional)"}
            </Label>
            {schema.enum ? (
              <Select value={typeof value === "string" ? value : ""} onValueChange={setValue}>
                <SelectTrigger id={`arg-${key}`} aria-label={key}>
                  <SelectValue placeholder="—" />
                </SelectTrigger>
                <SelectContent>
                  {schema.enum.map((v) => (
                    <SelectItem key={v} value={v}>
                      {v}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            ) : key === "collection" ? (
              <Select value={typeof value === "string" ? value : ""} onValueChange={setValue}>
                <SelectTrigger id={`arg-${key}`} aria-label={key}>
                  <SelectValue placeholder="any collection" />
                </SelectTrigger>
                <SelectContent>
                  {collections.length === 0 && (
                    <SelectItem value="_none">no collections</SelectItem>
                  )}
                  {collections.map((c) => (
                    <SelectItem key={c.id} value={c.id}>
                      {c.name}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            ) : schema.type === "boolean" ? (
              <Switch
                id={`arg-${key}`}
                checked={value === true}
                onCheckedChange={setValue}
                aria-label={key}
              />
            ) : schema.type === "number" || schema.type === "integer" ? (
              <Input
                id={`arg-${key}`}
                type="number"
                value={value === undefined || value === null ? "" : String(value)}
                onChange={(e) =>
                  setValue(e.target.value === "" ? undefined : Number(e.target.value))
                }
              />
            ) : key === "query" ? (
              <Input
                id={`arg-${key}`}
                placeholder={schema.description ?? ""}
                value={typeof value === "string" ? value : ""}
                onChange={(e) => setValue(e.target.value)}
              />
            ) : schema.type === "string" ? (
              <Textarea
                id={`arg-${key}`}
                placeholder={schema.description ?? ""}
                value={typeof value === "string" ? value : ""}
                onChange={(e) => setValue(e.target.value)}
                rows={2}
                className="font-mono text-xs"
              />
            ) : (
              <Input
                id={`arg-${key}`}
                placeholder={schema.description ?? key}
                value={typeof value === "string" ? value : ""}
                onChange={(e) => setValue(e.target.value)}
              />
            )}
          </div>
        );
      })}
    </div>
  );
}
