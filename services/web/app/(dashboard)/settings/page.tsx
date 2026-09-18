/** Settings (PRD §8.1 screen 7): pipeline config summary + MCP snippet. */

import { apiClient } from "@/lib/api-client";

export const dynamic = "force-dynamic";

export default async function SettingsPage() {
  const queues = await apiClient.queues();
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
        <pre>{`{
  "mcpServers": {
    "rag-platform": {
      "url": "https://your-host/mcp",
      "headers": { "Authorization": "Bearer <api-key>" }
    }
  }
}`}</pre>
      </div>
    </>
  );
}
