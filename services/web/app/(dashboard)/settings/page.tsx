/** Settings — WeKnora-style grouped sections: System Runtime, Pipeline &
 * Queues, MCP Integration. Static pipeline config + queue state + MCP
 * snippet; admin password note. */
import { headers } from "next/headers";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Separator } from "@/components/ui/separator";
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

function Row({ k, v }: { k: string; v: React.ReactNode }) {
  return (
    <li className="flex flex-wrap items-baseline gap-2 font-mono text-[12.5px]">
      <code className="rounded-sm bg-paper-deep px-1.5 py-0.5 font-mono text-[0.92em]">{k}</code>
      <span className="text-muted-foreground">{v}</span>
    </li>
  );
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
      {/* ===== section 1: System Runtime ===== */}
      <Card>
        <CardHeader>
          <CardTitle className="font-serif text-[19px] font-semibold">System Runtime</CardTitle>
          <CardDescription>
            Deployment-level configuration — set in the environment, not here (the web container is
            stateless, so runtime changes would not persist).
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-4">
          <ul className="m-0 list-none space-y-1.5">
            <Row
              k="ADMIN_PASSWORD_HASH"
              v="admin password — change it in the deployment environment"
            />
            <Row k="AUTH_JWT_SECRET" v="admin session signing key" />
            <Row k="SHARD_PAGES / MAX_DOCUMENT_PAGES" v="splitting and page cap" />
          </ul>
          <Separator />
          <p className="m-0 text-[11.5px] font-semibold tracking-[0.02em] text-muted-foreground">
            component state (live)
          </p>
          <ul className="m-0 flex flex-wrap gap-2 list-none p-0">
            {Object.entries(queues).map(([name]) => (
              <li key={name}>
                <code className="rounded-sm bg-paper-deep px-1.5 py-0.5 font-mono text-[0.92em]">
                  {name}
                </code>
              </li>
            ))}
          </ul>
        </CardContent>
      </Card>

      {/* ===== section 2: Pipeline & Queues ===== */}
      <Card className="mt-4">
        <CardHeader>
          <CardTitle className="font-serif text-[19px] font-semibold">
            Pipeline &amp; Queues
          </CardTitle>
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
          <p className="mt-3 mb-0 text-[11.5px] text-muted-foreground">
            Live lanes and in-flight lease holders: the{" "}
            <a href="/pipeline" className="text-press underline decoration-dotted">
              Pipeline &amp; Queues
            </a>{" "}
            page.
          </p>
        </CardContent>
      </Card>

      {/* ===== section 3: MCP Integration ===== */}
      <Card className="mt-4">
        <CardHeader>
          <CardTitle className="font-serif text-[19px] font-semibold">MCP Integration</CardTitle>
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
