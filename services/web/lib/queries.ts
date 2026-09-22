/** Typed TanStack Query hooks over the generated OpenAPI SDK.
 *
 * Liveness strategy: monitoring surfaces (dashboard, pipeline, logs) poll on
 * a short interval; document lists rely on window-focus refetch; mutations
 * invalidate their keys. Server components remain for first paint where the
 * page has no interactivity.
 */
"use client";

import { useQuery, useQueryClient } from "@tanstack/react-query";
import type { DocumentRow, ShardRow } from "@/lib/api-client";
import { apiClient } from "@/lib/api-client";

/** Monitoring pages poll; everything else uses staleTime + focus refetch. */
export const LIVE_INTERVAL_MS = 5_000;

export function useDocuments(params = "") {
  return useQuery({
    queryKey: ["documents", params],
    queryFn: () => apiClient.documents(params),
  });
}

export function useDocument(id: string) {
  return useQuery({
    queryKey: ["document", id],
    queryFn: () => apiClient.document(id),
  });
}

export function useShards(id: string) {
  return useQuery({
    queryKey: ["shards", id],
    queryFn: () => apiClient.shards(id),
  });
}

export function useHealth() {
  return useQuery({
    queryKey: ["health"],
    queryFn: () => apiClient.health(),
    refetchInterval: LIVE_INTERVAL_MS,
  });
}

export function useQueues() {
  return useQuery({
    queryKey: ["queues"],
    queryFn: () => apiClient.queues(),
    refetchInterval: LIVE_INTERVAL_MS,
  });
}

export function usePipeline() {
  return useQuery({
    queryKey: ["pipeline"],
    queryFn: () => apiClient.pipeline(),
    refetchInterval: LIVE_INTERVAL_MS,
  });
}

export function useEvents(params = "") {
  return useQuery({
    queryKey: ["events", params],
    queryFn: () => apiClient.events(params),
    refetchInterval: LIVE_INTERVAL_MS,
  });
}

export function useCollections() {
  return useQuery({
    queryKey: ["collections"],
    queryFn: () => apiClient.collections(),
  });
}

/** Model registry listing — the /models page and the collection create
 * dialog's model picker. Reads ride the admin proxy like every other
 * browser-side /v1 call. */
export function useEmbeddingModels() {
  return useQuery({
    queryKey: ["embedding-models"],
    queryFn: () => apiClient.models(),
  });
}

/** Chunk Inspector pagination (document detail → Chunk Inspector tab).
 * Calls the new REST path through the session-gated /api/admin proxy. */
export function useChunks(docId: string, page = 1, pageSize = 50) {
  return useQuery({
    queryKey: ["chunks", docId, page, pageSize],
    queryFn: () => apiClient.chunks(docId, page, pageSize),
  });
}

/** Reranker registry listing — the Models page's Rerankers tab and the
 * collection reranker bind dialog. */
export function useRerankModels() {
  return useQuery({
    queryKey: ["rerank-models"],
    queryFn: () => apiClient.rerankModels(),
  });
}

/** Re-exported for pages that still import row types from here. */
export type { DocumentRow, ShardRow };

/* ===== WeKnora revamp hooks (Phase 3) ===================================== */

/** KB detail header stats — byte_size + total_shards joined to the counts. */
export function useCollectionStats(id: string) {
  return useQuery({
    queryKey: ["collection-stats", id],
    queryFn: () => apiClient.collectionStats(id),
  });
}

/** KB-wide chunk browser (Chunk Inspector tab) with optional doc narrowing. */
export function useCollectionChunks(
  collectionId: string,
  docId: string | undefined,
  page = 1,
  pageSize = 50,
) {
  return useQuery({
    queryKey: ["collection-chunks", collectionId, docId ?? null, page, pageSize],
    queryFn: () => apiClient.collectionChunks(collectionId, docId, page, pageSize),
  });
}

/** Env-configured runtime constants — chunking preview + retry-ladder strip. */
export function useSystemSettings() {
  return useQuery({
    queryKey: ["system-settings"],
    queryFn: () => apiClient.systemSettings(),
  });
}

/** In-KB retrieval test — pinned single-collection search, run on demand
 * (enabled:false keeps the query idle until the panel fires runSearch). */
export function useSearchTest(collectionId: string, query: string, topK: number, rerank: boolean) {
  return useQuery({
    queryKey: ["search-test", collectionId, query, topK, rerank],
    queryFn: () =>
      apiClient.search({
        query,
        collection: collectionId,
        top_k: topK,
        rerank,
      }),
    enabled: false,
  });
}

/** Batch delete / batch re-parse mutation helper — invalidates the KB's
 * document + stats keys on success. */
export function useBatchDocuments(collectionId: string) {
  const qc = useQueryClient();
  return async (action: "delete" | "reparse", docIds: string[]) => {
    const res = await apiClient.batchDocuments(action, docIds);
    await qc.invalidateQueries({ queryKey: ["documents"] });
    await qc.invalidateQueries({ queryKey: ["collection-stats", collectionId] });
    return res;
  };
}

/** Single-doc delete mutation helper — same invalidations as batch. */
export function useDeleteDocument(collectionId: string) {
  const qc = useQueryClient();
  return async (docId: string) => {
    const res = await apiClient.deleteDocument(docId);
    await qc.invalidateQueries({ queryKey: ["documents"] });
    await qc.invalidateQueries({ queryKey: ["collection-stats", collectionId] });
    return res;
  };
}
