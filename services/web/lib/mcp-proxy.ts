/** MCP JSON-RPC client for the Playground proxy — speaks streamable HTTP to
 * the real MCP server (http://mcp:8430/mcp) on behalf of the browser.
 *
 * Protocol-true by design (ADR 0003): the playground tests exactly what an
 * MCP client sees — tool descriptions, top_k clamps, collection scoping —
 * not a REST mirror. Only three methods are used (initialize, tools/list,
 * tools/call), so no MCP SDK dependency; this keeps the proxy deterministic
 * and the web deps light.
 *
 * Auth: `Authorization: Bearer $MCP_API_KEY`, held server-side only. The
 * browser talks to /api/playground (app/api/playground/route.ts), which
 * session-gates first.
 *
 * Responses may be `application/json` OR `text/event-stream` (streamable
 * HTTP replies SSE per JSON-RPC message) — parseUpstreamBody handles both.
 * FastMCP issues `Mcp-Session-Id`; we cache it per server process and
 * re-initialize transparently on 404/session-expiry.
 */

const MCP_URL = process.env.MCP_API_URL ?? "http://mcp:8430/mcp";
const MCP_API_KEY = process.env.MCP_API_KEY ?? "";

const PROTOCOL_VERSION = "2025-06-18";
const CLIENT_INFO = { name: "lybrix-playground", version: "0.1.0" };

export interface McpTool {
  name: string;
  description?: string;
  inputSchema?: {
    type: string;
    properties?: Record<string, unknown>;
    required?: string[];
    [k: string]: unknown;
  };
}

interface JsonRpcResponse {
  jsonrpc: "2.0";
  id: number;
  result?: unknown;
  error?: { code: number; message: string; data?: unknown };
}

export class McpUpstreamError extends Error {
  constructor(
    message: string,
    public readonly status?: number,
  ) {
    super(message);
    this.name = "McpUpstreamError";
  }
}

// Per-process session cache. Route handlers share the module scope, so one
// upstream session lives per web server process, not per request.
let sessionId: string | null = null;
let nextId = 1;

/** Accept header: streamable HTTP requires both content types offered. */
const ACCEPT = "application/json, text/event-stream";

async function rpc(method: string, params?: unknown): Promise<JsonRpcResponse> {
  const id = nextId++;
  const started = performance.now();
  const res = await fetch(MCP_URL, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      accept: ACCEPT,
      authorization: `Bearer ${MCP_API_KEY}`,
      "mcp-protocol-version": PROTOCOL_VERSION,
      ...(sessionId ? { "mcp-session-id": sessionId } : {}),
    },
    body: JSON.stringify({ jsonrpc: "2.0", id, method, params: params ?? {} }),
    cache: "no-store",
  });

  if (res.status === 404) {
    // Session expired/unknown upstream — drop it; caller may retry once.
    sessionId = null;
    throw new McpUpstreamError("MCP session not found (upstream restarted)", 404);
  }
  if (res.status === 401) {
    throw new McpUpstreamError("MCP_API_KEY rejected by the MCP server", 401);
  }
  if (!res.ok) {
    const detail = (await res.text()).slice(0, 300);
    throw new McpUpstreamError(`MCP upstream ${res.status}: ${detail}`, res.status);
  }

  const sid = res.headers.get("mcp-session-id");
  if (sid) sessionId = sid;

  const msg = await parseUpstreamBody(res, id);
  return { ...msg, latencyHintMs: performance.now() - started } as JsonRpcResponse & {
    latencyHintMs: number;
  };
}

/** Streamable HTTP answers one JSON-RPC message per POST, either as plain
 * JSON or wrapped in an SSE stream. Read the full body either way — tool
 * calls are bounded (PRD §7.2), so nothing streams indefinitely here. */
async function parseUpstreamBody(res: Response, id: number): Promise<JsonRpcResponse> {
  const text = await res.text();
  const contentType = res.headers.get("content-type") ?? "";

  if (contentType.includes("text/event-stream")) {
    for (const line of text.split("\n")) {
      if (!line.startsWith("data:")) continue;
      const payload = line.slice(5).trim();
      if (!payload) continue;
      let frame: JsonRpcResponse;
      try {
        frame = JSON.parse(payload) as JsonRpcResponse;
      } catch {
        continue;
      }
      if (frame.id === id && (frame.result !== undefined || frame.error !== undefined)) {
        return frame;
      }
    }
    throw new McpUpstreamError("SSE response carried no matching JSON-RPC result");
  }

  try {
    return JSON.parse(text) as JsonRpcResponse;
  } catch {
    throw new McpUpstreamError("MCP upstream returned a non-JSON-RPC body");
  }
}

/** `initialize` handshake. Idempotent per process: skips when a live session
 * is already cached; the caller retries via ensureSession() on 404. */
async function initializeOnce(): Promise<void> {
  if (sessionId) return;
  const res = await rpc("initialize", {
    protocolVersion: PROTOCOL_VERSION,
    capabilities: {},
    clientInfo: CLIENT_INFO,
  });
  if (res.error) {
    throw new McpUpstreamError(`MCP initialize failed: ${res.error.message}`);
  }
  // FastMCP requires `notifications/initialized` before other methods.
  await fetch(MCP_URL, {
    method: "POST",
    headers: {
      "content-type": "application/json",
      accept: ACCEPT,
      authorization: `Bearer ${MCP_API_KEY}`,
      "mcp-protocol-version": PROTOCOL_VERSION,
      ...(sessionId ? { "mcp-session-id": sessionId } : {}),
    },
    body: JSON.stringify({
      jsonrpc: "2.0",
      method: "notifications/initialized",
      params: {},
    }),
    cache: "no-store",
  }).catch(() => {});
}

/** rpc + one transparent retry after a session-expiry 404. */
async function ensureRpc(method: string, params?: unknown): Promise<JsonRpcResponse> {
  await initializeOnce();
  try {
    return await rpc(method, params);
  } catch (e) {
    if (e instanceof McpUpstreamError && e.status === 404) {
      sessionId = null;
      await initializeOnce();
      return await rpc(method, params);
    }
    throw e;
  }
}

export interface McpCallResult {
  result: unknown | null;
  isError: boolean;
  raw: JsonRpcResponse | null;
  latencyMs: number;
}

/** tools/list — schemas + descriptions for the playground UI. */
export async function mcpToolsList(): Promise<McpTool[]> {
  const res = await ensureRpc("tools/list", {});
  if (res.error) {
    throw new McpUpstreamError(`tools/list failed: ${res.error.message}`);
  }
  const tools = (res.result as { tools?: McpTool[] } | undefined)?.tools ?? [];
  return tools;
}

/** tools/call — the playground's run button. */
export async function mcpToolsCall(
  name: string,
  args: Record<string, unknown>,
): Promise<McpCallResult> {
  const started = performance.now();
  const res = await ensureRpc("tools/call", { name, arguments: args });
  if (res.error) {
    // JSON-RPC error frame (unknown tool, bad args) — still protocol-true.
    return {
      result: { error: res.error.message, data: res.error.data ?? null },
      isError: true,
      raw: res,
      latencyMs: performance.now() - started,
    };
  }
  const result = res.result as { isError?: boolean; content?: unknown } | undefined;
  return {
    result: res.result ?? null,
    isError: result?.isError === true,
    raw: res,
    latencyMs: performance.now() - started,
  };
}
