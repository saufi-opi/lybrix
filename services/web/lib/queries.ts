/** Typed TanStack Query hooks over the generated OpenAPI SDK.
 *
 * Liveness strategy: monitoring surfaces (dashboard, pipeline, logs) poll on
 * a short interval; document lists rely on window-focus refetch; mutations
 * invalidate their keys. Server components remain for first paint where the
 * page has no interactivity.
 */
"use client";

import { useQuery } from "@tanstack/react-query";
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

/** Re-exported for pages that still import row types from here. */
export type { DocumentRow, ShardRow };
