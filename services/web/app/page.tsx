/** Dashboard (PRD §8.1 screen 1): four counters, queue depth, worker strip. */

import { apiClient } from "@/lib/api-client";

export const dynamic = "force-dynamic";

export default async function DashboardPage() {
  const [docs, queues, health] = await Promise.all([
    apiClient.documents("?limit=200"),
    apiClient.queues(),
    apiClient.health(),
  ]);

  const ready = docs.filter((d) => d.state === "ready").length;
  const inflight = docs.filter((d) =>
    ["splitting", "parsing", "embedding", "indexing", "uploaded"].includes(d.state)
  ).length;
  const failed = docs.filter((d) => d.state === "failed").length;
  const partial = docs.filter((d) => d.state === "partial").length;

  return (
    <>
      <div className="grid4">
        <div className="panel counter">
          <div className="num">{ready}</div>
          <div className="muted">ready</div>
        </div>
        <div className="panel counter">
          <div className="num">{inflight}</div>
          <div className="muted">in-flight</div>
        </div>
        <div className="panel counter">
          <div className="num">{failed}</div>
          <div className="muted">failed</div>
        </div>
        <div className="panel counter">
          <div className="num">{partial}</div>
          <div className="muted">partial</div>
        </div>
      </div>

      <div className="panel">
        <h3>Queue depth</h3>
        <table>
          <thead>
            <tr>
              <th>stream</th>
              <th>length</th>
              <th>pending</th>
            </tr>
          </thead>
          <tbody>
            {Object.entries(queues).map(([name, q]) => (
              <tr key={name}>
                <td>{name}</td>
                <td>{q.length ?? "—"}</td>
                <td>{q.pending ?? "—"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <div className="panel">
        <h3>Dependencies</h3>
        <p className="muted">
          {Object.entries(health)
            .map(([k, v]) => `${k}: ${v}`)
            .join(" · ")}
        </p>
      </div>
    </>
  );
}
