/** Document detail (PRD §8.1 screen 3): shard grid, retry buttons. */

import { apiClient } from "@/lib/api-client";
import { ShardGrid } from "@/components/shard-grid";
import { RetryButtons } from "@/components/retry-buttons";

export const dynamic = "force-dynamic";

export default async function DocumentDetailPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = await params;
  const [doc, shards] = await Promise.all([
    apiClient.document(id),
    apiClient.shards(id),
  ]);

  return (
    <>
      <div className="panel">
        <h2>{doc.title ?? id}</h2>
        <p>
          <span className={`badge ${doc.state}`}>{doc.state}</span>
          {doc.completeness != null && (
            <span className="muted"> · completeness {Math.round(doc.completeness * 100)}%</span>
          )}
          {doc.error_code && <span className="muted"> · last error {doc.error_code}</span>}
        </p>
        <RetryButtons docId={id} />
      </div>

      <div className="panel">
        <h3>Shard grid</h3>
        <ShardGrid shards={shards} />
      </div>

      <div className="panel">
        <h3>Shards</h3>
        <table>
          <thead>
            <tr>
              <th>idx</th>
              <th>pages</th>
              <th>state</th>
              <th>attempts</th>
              <th>duration</th>
              <th>peak RSS</th>
              <th>error</th>
            </tr>
          </thead>
          <tbody>
            {shards.map((s) => (
              <tr key={s.idx}>
                <td>{s.idx}</td>
                <td>
                  {s.page_start}–{s.page_end}
                </td>
                <td>{s.state}</td>
                <td>{s.attempts}</td>
                <td>{s.duration_ms != null ? `${(s.duration_ms / 1000).toFixed(1)}s` : "—"}</td>
                <td>{s.peak_rss_mb != null ? `${s.peak_rss_mb}MB` : "—"}</td>
                <td>{s.error_code ?? "—"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </>
  );
}
