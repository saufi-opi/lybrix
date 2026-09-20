/** MCP Playground proxy (ADR 0003): the browser talks to /api/playground,
 * this handler session-gates, then speaks MCP JSON-RPC to the real MCP
 * server (http://mcp:8430/mcp) with `Authorization: Bearer $MCP_API_KEY`.
 * The key never reaches the client — same trust pattern as the admin proxy.
 *
 * GET  → tools/list (schemas + descriptions for the UI)
 * POST → tools/call { tool: string, args: object } → { result, isError, latencyMs }
 *
 * Protocol-true: responses are exactly what an MCP client sees, including
 * tool descriptions, top_k clamps and collection scoping. Only the six
 * read-only tools are reachable through here.
 */
import { type NextRequest, NextResponse } from "next/server";
import { SESSION_COOKIE, verifySession } from "@/lib/auth";
import { McpUpstreamError, mcpToolsCall, mcpToolsList } from "@/lib/mcp-proxy";

export const runtime = "nodejs";

export async function GET(req: NextRequest) {
  const user = await verifySession(req.cookies.get(SESSION_COOKIE)?.value);
  if (!user) {
    return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  }
  try {
    const tools = await mcpToolsList();
    return NextResponse.json({ tools });
  } catch (e) {
    return mcpErrorResponse(e);
  }
}

export async function POST(req: NextRequest) {
  const user = await verifySession(req.cookies.get(SESSION_COOKIE)?.value);
  if (!user) {
    return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  }
  let body: { tool?: unknown; args?: unknown };
  try {
    body = (await req.json()) as { tool?: unknown; args?: unknown };
  } catch {
    return NextResponse.json({ error: "invalid JSON body" }, { status: 400 });
  }
  const tool = typeof body.tool === "string" ? body.tool : null;
  const args =
    body.args !== null && typeof body.args === "object" && !Array.isArray(body.args)
      ? (body.args as Record<string, unknown>)
      : {};
  if (!tool) {
    return NextResponse.json({ error: "missing tool name" }, { status: 400 });
  }
  try {
    const call = await mcpToolsCall(tool, args);
    return NextResponse.json({
      result: call.result,
      isError: call.isError,
      latencyMs: Math.round(call.latencyMs),
    });
  } catch (e) {
    return mcpErrorResponse(e);
  }
}

function mcpErrorResponse(e: unknown) {
  if (e instanceof McpUpstreamError) {
    const status = e.status === 401 || e.status === 404 ? e.status : 502;
    return NextResponse.json({ error: e.message }, { status });
  }
  return NextResponse.json(
    { error: e instanceof Error ? e.message : "MCP proxy failure" },
    { status: 502 },
  );
}
