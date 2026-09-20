/** Settings (PRD §8.1 screen 7): pipeline config summary + MCP snippet. */

import { headers } from "next/headers";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
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
        lybrix: {
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
      <Card>
        <CardHeader>
          <CardTitle className="font-serif text-[19px] font-semibold">Pipeline config</CardTitle>
          <CardDescription>
            Shard size, OCR threshold, retry ladder and backlog cap are env-configured
            (core.config.Settings is the single source of truth). Queue state:
          </CardDescription>
        </CardHeader>
        <CardContent>
          <ul className="m-0 list-none space-y-1">
            {Object.entries(queues).map(([name, q]) => (
              <li key={name} className="font-mono text-[12.5px]">
                <code className="rounded-sm bg-paper-deep px-1.5 py-0.5 font-mono text-[0.92em]">
                  {name}
                </code>{" "}
                — length {q.length ?? "—"}, pending {q.pending ?? "—"}
              </li>
            ))}
          </ul>
        </CardContent>
      </Card>

      <Card className="mt-4">
        <CardHeader>
          <CardTitle className="font-serif text-[19px] font-semibold">Admin password</CardTitle>
          <CardDescription>
            The admin password is set via the{" "}
            <code className="rounded-sm bg-paper-deep px-1.5 py-0.5 font-mono text-[0.92em]">
              ADMIN_PASSWORD_HASH
            </code>{" "}
            environment variable on the server — change it in the deployment environment, not here
            (the web container is stateless, so runtime changes would not persist).
          </CardDescription>
        </CardHeader>
      </Card>

      <Card className="mt-4">
        <CardHeader>
          <CardTitle className="font-serif text-[19px] font-semibold">MCP connection</CardTitle>
          <CardDescription>
            This deployment serves MCP at{" "}
            <code className="rounded-sm bg-paper-deep px-1.5 py-0.5 font-mono text-[0.92em]">
              {mcp}
            </code>{" "}
            — use it as the server URL in your MCP client, with any of the API keys from the API
            Keys page.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <pre className="m-0 overflow-x-auto rounded border border-rail-edge bg-rail px-3 py-2.5 font-mono text-[12.5px] whitespace-pre text-[#cde3d6]">
            {mcpConfig}
          </pre>
        </CardContent>
      </Card>
    </>
  );
}
