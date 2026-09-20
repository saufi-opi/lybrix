/** Documents list (PRD §8.1 screen 2): filters, progress, state badges. */

import { ProgressBar } from "@/components/progress-bar";
import { apiClient } from "@/lib/api-client";

export const dynamic = "force-dynamic";

export default async function DocumentsPage({
  searchParams,
}: {
  searchParams: Promise<{ state?: string; q?: string }>;
}) {
  const params = await searchParams;
  const sp = new URLSearchParams();
  if (params.state) sp.set("state", params.state);
  if (params.q) sp.set("q", params.q);
  const docs = await apiClient.documents(`?${sp.toString()}`);

  return (
    <>
      <form className="panel" method="get">
        <input name="q" placeholder="Search title…" defaultValue={params.q} />
        <select name="state" defaultValue={params.state ?? ""}>
          <option value="">any state</option>
          {[
            "uploaded",
            "splitting",
            "parsing",
            "embedding",
            "ready",
            "partial",
            "failed",
            "archived",
          ].map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
        <button type="submit">Filter</button>
      </form>

      <div className="panel">
        <table>
          <thead>
            <tr>
              <th>title</th>
              <th>pages</th>
              <th>state</th>
              <th>progress</th>
              <th>completeness</th>
              <th>updated</th>
            </tr>
          </thead>
          <tbody>
            {docs.map((d) => {
              const pct =
                d.total_shards && d.total_shards > 0
                  ? Math.round(((d.shards_done + d.shards_failed) / d.total_shards) * 75)
                  : 0;
              return (
                <tr key={d.id}>
                  <td>
                    <a href={`/documents/${d.id}`}>{d.title ?? d.id}</a>
                  </td>
                  <td>{d.page_count ?? "—"}</td>
                  <td>
                    <span className={`badge ${d.state}`}>{d.state}</span>
                  </td>
                  <td style={{ minWidth: 120 }}>
                    <ProgressBar
                      pct={pct}
                      label={`${d.shards_done}/${d.total_shards ?? "?"} shards`}
                    />
                  </td>
                  <td>{d.completeness != null ? `${Math.round(d.completeness * 100)}%` : "—"}</td>
                  <td className="muted">{new Date(d.updated_at).toLocaleString()}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </>
  );
}
