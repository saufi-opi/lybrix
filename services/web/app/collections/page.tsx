/** Collections (PRD §8.1 screen 6) and Settings (§8.1 screen 7). */

import { apiClient } from "@/lib/api-client";

export const dynamic = "force-dynamic";

export default async function CollectionsPage() {
  const cols = await apiClient.collections();
  return (
    <div className="panel">
      <h2>Collections</h2>
      <p className="muted">
        Embedding model and dimension are immutable at creation — changing a model means a new
        collection and a re-embed migration (§8.1).
      </p>
      <table>
        <thead>
          <tr>
            <th>id</th>
            <th>name</th>
            <th>model</th>
            <th>docs</th>
          </tr>
        </thead>
        <tbody>
          {cols.map((c) => (
            <tr key={String(c.id)}>
              <td>{String(c.id)}</td>
              <td>{String(c.name)}</td>
              <td>{String(c.embedding_model)}</td>
              <td>{String(c.doc_count ?? "—")}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
