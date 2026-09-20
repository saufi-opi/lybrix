/** Logs (PRD §8.1 screen 5): unified filterable event stream. */

import { apiClient } from "@/lib/api-client";

export const dynamic = "force-dynamic";

export default async function LogsPage({
  searchParams,
}: {
  searchParams: Promise<{ level?: string; stage?: string; doc_id?: string }>;
}) {
  const params = await searchParams;
  const sp = new URLSearchParams();
  if (params.level) sp.set("level", params.level);
  if (params.doc_id) sp.set("doc_id", params.doc_id);
  const events = await apiClient.events(`?${sp.toString()}`);

  return (
    <div className="panel">
      <h2>Events</h2>
      <table>
        <thead>
          <tr>
            <th>time</th>
            <th>level</th>
            <th>stage</th>
            <th>code</th>
            <th>message</th>
            <th>document</th>
          </tr>
        </thead>
        <tbody>
          {events.map((e) => (
            <tr key={String(e.id)}>
              <td className="muted">{new Date(String(e.created_at)).toLocaleString()}</td>
              <td>{String(e.level)}</td>
              <td>{String(e.stage ?? "—")}</td>
              <td>{String(e.code ?? "—")}</td>
              <td>{String(e.message)}</td>
              <td>{e.doc_id ? <a href={`/documents/${String(e.doc_id)}`}>open</a> : "—"}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
