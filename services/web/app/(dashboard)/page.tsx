/** Dashboard (PRD §8.1 screen 1): counters, queue depth, worker strip.
 *
 * Counts come from /v1/system/pipeline (full-table aggregate + full doc-state
 * breakdown), NOT from the ?limit=200 documents list — the old version counted
 * states over the first 200 rows only, so "ready" disagreed with the pipeline
 * page (74 vs 93) and silently stopped growing once >200 docs existed.
 */

import { apiClient } from "@/lib/api-client";

export const dynamic = "force-dynamic";

export default async function DashboardPage() {
  const [pipeline, queues, health] = await Promise.all([
    apiClient.pipeline(),
    apiClient.queues(),
    apiClient.health(),
  ]);

  const c = (pipeline.counts ?? {}) as Record<string, unknown>;
  // Authoritative per-state counts from GROUP BY state (server-side aggregate).
  const states = (c.docs_states ?? {}) as Record<string, number>;
  const ready = states.ready ?? 0;
  const failed = (c.docs_failed as number) ?? 0;
  const partial = states["partial"] ?? 0;
  // Phase breakdown of the old ambiguous "in-flight" card (2026-09-12):
  // parsing_active = shards still converting; awaiting_embed = whole-book
  // settled, waiting on the embedder. Both are non-terminal doc counts —
  // job-level in-flight (Redis PEL) lives on the pipeline page.
  const parsingActive = (c.docs_parsing_active as number) ?? null;
  const awaitingEmbed = (c.docs_awaiting_embed as number) ?? null;
  const activeDocs =
    (states["uploaded"] ?? 0) +
    (states["splitting"] ?? 0) +
    (states["parsing"] ?? 0) +
    (states["embedding"] ?? 0) +
    (states["indexing"] ?? 0);
  const total = (c.docs_total as number) ?? 0;

  return (
    <>
      <div className="grid4">
        <div className="panel counter">
          <div className="num">{ready}</div>
          <div className="muted">ready{total ? ` / ${total}` : ""}</div>
        </div>
        <div className="panel counter">
          <div className="num">
            {parsingActive !== null ? (
              <>
                {parsingActive}
                <span className="muted" style={{ fontSize: "0.55em" }}>
                  {" "}
                  + {awaitingEmbed ?? 0} queued
                </span>
              </>
            ) : (
              activeDocs
            )}
          </div>
          <div className="muted">parsing active (+ awaiting embed)</div>
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
              <th>undelivered</th>
              <th>pending (PEL)</th>
              <th>history (XLEN)</th>
            </tr>
          </thead>
          <tbody>
            {Object.entries(queues).map(([name, q]) => (
              <tr key={name}>
                <td>{name}</td>
                <td>{q.undelivered ?? "—"}</td>
                <td>{q.pending ?? "—"}</td>
                <td className="muted">{q.length ?? "—"}</td>
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
