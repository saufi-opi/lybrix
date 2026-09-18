/** Settings (PRD §8.1 screen 7): pipeline config summary + MCP snippet. */

import { headers } from "next/headers";
import { apiClient } from "@/lib/api-client";

export const dynamic = "force-dynamic";

/** The MCP endpoint is served by the same ingress as this UI (Caddy routes
 * /mcp to the mcp service), so the real host is the request's own Host —
 * works in compose (localhost:3000), behind Caddy (PUBLIC_HOST), or wherever
 * the UI is browsed from. */
async function mcpUrl(): Promise<string> {
  const h = await headers();
  const host = h.get("x-forwarded-host") ?? h.get("host") ?? "localhost:3000";
  const proto = h.get("x-forwarded-proto") ?? (host.startsWith("localhost") ? "http" : "https");
  return `${proto}://${host}/mcp`;
}

export default async function SettingsPage() {
  const [queues, mcp] = await Promise.all([apiClient.queues(), mcpUrl()]);
  const mcpConfig = JSON.stringify(
    {
      mcpServers: {
        "lybrix": {
          url: mcp,
          headers: { Authorization: "Bearer <api-key>" },
        },
      },
    },
    null,
    2,
  );
  return (
    <>
      <div className="panel">
        <h2>Pipeline config</h2>
        <p className="muted">
          Shard size, OCR threshold, retry ladder and backlog cap are env-configured
          (core.config.Settings is the single source of truth). Queue state:
        </p>
        <ul>
          {Object.entries(queues).map(([name, q]) => (
            <li key={name}>
              <code>{name}</code> — length {q.length ?? "—"}, pending {q.pending ?? "—"}
            </li>
          ))}
        </ul>
      </div>
      <div className="panel">
        <h2>Admin password</h2>
        <p className="muted">
          The admin password is set via the <code>ADMIN_PASSWORD_HASH</code> environment
          variable on the server — change it in the deployment environment, not here
          (the web container is stateless, so runtime changes would not persist).
        </p>
      </div>
      <div className="panel">
        <h2>MCP connection</h2>
        <p className="muted">
          This deployment serves MCP at <code>{mcp}</code> — use it as the server URL in
          your MCP client, with any of the API keys from the API Keys page.
        </p>
        <pre className="mcp-box">{mcpConfig}</pre>
      </div>
    </>
  );
}
