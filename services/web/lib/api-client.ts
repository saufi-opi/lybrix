/** Typed client for the control-plane API (PRD §9). */

const API_URL = process.env.API_URL ?? "http://localhost:8000";

async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${API_URL}${path}`, {
    ...init,
    headers: { "content-type": "application/json", ...(init?.headers ?? {}) },
    cache: "no-store",
  });
  if (!res.ok) {
    const detail = await res.text();
    throw new Error(`API ${res.status}: ${detail.slice(0, 200)}`);
  }
  return res.json() as Promise<T>;
}

export interface DocumentRow {
  id: string;
  title: string | null;
  collection_id: string | null;
  page_count: number | null;
  state: string;
  total_shards: number | null;
  shards_done: number;
  shards_failed: number;
  completeness: number | null;
  error_code: string | null;
  updated_at: string;
}

export interface ShardRow {
  idx: number;
  page_start: number;
  page_end: number;
  state: string;
  attempts: number;
  needs_ocr: boolean;
  duration_ms: number | null;
  peak_rss_mb: number | null;
  error_code: string | null;
}

export const apiClient = {
  documents: (params = "") => api<DocumentRow[]>(`/v1/documents${params}`),
  document: (id: string) => api<DocumentRow>(`/v1/documents/${id}`),
  shards: (id: string) => api<ShardRow[]>(`/v1/documents/${id}/shards`),
  health: () => api<Record<string, string>>("/v1/system/health"),
  queues: () => api<Record<string, { length: number | null; pending: number | null }>>("/v1/system/queues"),
  events: (params = "") => api<Record<string, unknown>[]>(`/v1/events${params}`),
  collections: () => api<Record<string, unknown>[]>("/v1/collections"),
  retry: (id: string, scope: string) =>
    api<{ retried: string }>(`/v1/documents/${id}/retry`, {
      method: "POST",
      body: JSON.stringify({ scope }),
    }),
};
