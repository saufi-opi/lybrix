/** Typed client for the control-plane API (PRD §9).
 *
 * Now a thin facade over the generated OpenAPI client (lib/client/, from
 * @hey-api/openapi-ts). Base-URL wiring lives in lib/oid-client.ts (imported
 * for its side effect). Existing pages keep this interface; new code should
 * call the generated SDK / lib/queries.ts directly and drop the wrappers.
 */

// Side effect: configures the generated client's baseUrl (server = API_URL,
// browser = same-origin rewrite). Must run before any SDK call.
import "@/lib/oid-client";

import {
  collectionStatsV1CollectionsCollectionIdStatsGet,
  deleteDocumentV1DocumentsDocIdDelete,
  getDocumentV1DocumentsDocIdGet,
  getShardsV1DocumentsDocIdShardsGet,
  healthV1SystemHealthGet,
  listCollectionChunksV1CollectionsCollectionIdChunksGet,
  listCollectionsV1CollectionsGet,
  listDocumentChunksV1DocumentsDocIdChunksGet,
  listDocumentsV1DocumentsGet,
  listEventsV1EventsGet,
  listModelsV1ModelsGet,
  listRerankModelsV1RerankModelsGet,
  pipelineV1SystemPipelineGet,
  queuesV1SystemQueuesGet,
  retryV1DocumentsDocIdRetryPost,
  systemSettingsV1SystemSettingsGet,
} from "@/lib/client/sdk.gen";
import type {
  ChunkList,
  ChunkOut,
  DocumentOut,
  ModelOut,
  RerankModelOut,
  ShardOut,
} from "@/lib/client/types.gen";

/** Re-exported so existing imports of the hand-written types keep working. */
export type DocumentRow = DocumentOut;
export type ShardRow = ShardOut;
export type ChunkRow = ChunkOut;
export type RerankerRow = RerankModelOut;
export type ChunkListPage = ChunkList;

/** throwOnError makes the SDK reject non-2xx instead of returning the error
 * envelope — matches the old api<T>() contract of throwing on !res.ok. */
const THROW = { throwOnError: true } as const;

/** Read endpoints gated with require_scope("search") (R-21 hardening, review
 * minor 2): browser calls to them ride the session-gated same-origin admin
 * proxy /api/admin/v1/* (handler: app/api/admin/[...path]/route.ts — verifies
 * the session cookie, attaches Bearer $API_ADMIN_KEY server-side; the same-
 * origin rewrite carries no header). Server components keep the direct /v1/*
 * paths via API_URL — their bearer key is held server-side. */
const isBrowser = typeof window !== "undefined";

/** Per-call override: baseUrl "" (same-origin) + a URL the generated client
 * appends its path to — the SDK path constants are /v1/..., so we point the
 * request at /api/admin/v1/... via a baseUrl swap. */
function viaAdminProxy<T>(call: (baseUrl: string) => Promise<T>): Promise<T> {
  if (!isBrowser) {
    return call("");
  }
  return call("/api/admin");
}

/** Per-collection stats readout shape (openapi CollectionStats). */
export interface CollectionStats {
  id: string;
  name: string;
  embedding_model: string;
  doc_count: number;
  chunk_count: number;
  byte_size: number;
  total_shards: number;
}

/** GET /v1/system/settings readout (openapi SystemSettings). */
export interface SystemSettings {
  parent_tokens: number;
  parent_hard_cap: number;
  child_tokens: number;
  child_stride_tokens: number;
  shard_pages: number;
  min_yield_chars_per_page: number;
  shard_lease_seconds: number;
  max_shard_attempts: number;
  max_parse_backlog: number;
  max_document_pages: number;
  embed_max_attempts: number;
  search_default_top_k: number;
  search_max_top_k: number;
  rerank_candidates: number;
}

/** One /v1/search hit (search.py mapping). */
export interface SearchHitRow {
  chunk_id: string;
  doc_id: string;
  doc_title: string | null;
  page_start: number | null;
  page_end: number | null;
  heading_path: string[];
  text: string;
  score: number;
  partial: boolean;
  reranked?: boolean;
}

export const apiClient = {
  documents: (params = "") => {
    const qs = new URLSearchParams(params.replace(/^\?/, ""));
    return viaAdminProxy((baseUrl) =>
      listDocumentsV1DocumentsGet({
        query: Object.fromEntries(qs),
        ...THROW,
        baseUrl,
      }),
    ).then((r) => r.data as DocumentRow[]);
  },
  document: (id: string) =>
    viaAdminProxy((baseUrl) =>
      getDocumentV1DocumentsDocIdGet({ path: { doc_id: id }, ...THROW, baseUrl }),
    ).then((r) => r.data as DocumentRow),
  shards: (id: string) =>
    viaAdminProxy((baseUrl) =>
      getShardsV1DocumentsDocIdShardsGet({ path: { doc_id: id }, ...THROW, baseUrl }),
    ).then((r) => r.data as ShardRow[]),
  health: () => healthV1SystemHealthGet(THROW).then((r) => r.data as Record<string, string>),
  queues: () =>
    queuesV1SystemQueuesGet(THROW).then(
      (r) =>
        r.data as Record<
          string,
          { length: number | null; pending: number | null; undelivered: number | null }
        >,
    ),
  pipeline: () => pipelineV1SystemPipelineGet(THROW).then((r) => r.data as Record<string, unknown>),
  events: (params = "") => {
    const qs = new URLSearchParams(params.replace(/^\?/, ""));
    return listEventsV1EventsGet({
      query: Object.fromEntries(qs),
      ...THROW,
    }).then((r) => r.data as Record<string, unknown>[]);
  },
  collections: () =>
    viaAdminProxy((baseUrl) => listCollectionsV1CollectionsGet({ ...THROW, baseUrl })).then(
      (r) => r.data as Record<string, unknown>[],
    ),
  models: () =>
    viaAdminProxy((baseUrl) => listModelsV1ModelsGet({ ...THROW, baseUrl })).then(
      (r) => r.data as unknown as ModelOut[],
    ),
  rerankModels: () =>
    viaAdminProxy((baseUrl) => listRerankModelsV1RerankModelsGet({ ...THROW, baseUrl })).then(
      (r) => r.data as unknown as RerankModelOut[],
    ),
  chunks: (docId: string, page = 1, pageSize = 50) =>
    viaAdminProxy((baseUrl) =>
      listDocumentChunksV1DocumentsDocIdChunksGet({
        path: { doc_id: docId },
        query: { page, page_size: pageSize },
        ...THROW,
        baseUrl,
      }),
    ).then((r) => r.data as unknown as ChunkList),
  retry: (id: string, scope: string) =>
    retryV1DocumentsDocIdRetryPost({
      path: { doc_id: id },
      body: { scope },
      ...THROW,
    }).then((r) => r.data as { retried: string }),
  /** Single-doc delete (Phase 1) — cascades shards/chunks; the raw MinIO
   * object stays for janitor GC. Rides the admin proxy in the browser. */
  deleteDocument: (id: string) =>
    viaAdminProxy((baseUrl) =>
      deleteDocumentV1DocumentsDocIdDelete({ path: { doc_id: id }, ...THROW, baseUrl }),
    ).then((r) => r.data as { id: string; deleted: boolean }),
  /** Batch delete / batch re-parse (Phase 1) — fail-closed: the API rejects
   * the whole batch (422 unknown id / 403 out-of-scope collection) before
   * applying anything. */
  batchDocuments: (action: "delete" | "reparse", docIds: string[]) =>
    viaAdminProxy(async (baseUrl) => {
      const { documentBatchV1DocumentsBatchPost } = await import("@/lib/client/sdk.gen");
      return documentBatchV1DocumentsBatchPost({
        body: { action, doc_ids: docIds },
        ...THROW,
        baseUrl,
      });
    }).then(
      (r) =>
        r.data as {
          action: string;
          results: Array<{ doc_id: string; status: string; detail?: string }>;
        },
    ),
  /** Per-collection stats readout (Phase 1: byte_size + total_shards added). */
  collectionStats: (id: string) =>
    viaAdminProxy((baseUrl) =>
      collectionStatsV1CollectionsCollectionIdStatsGet({
        path: { collection_id: id },
        ...THROW,
        baseUrl,
      }),
    ).then((r) => r.data as unknown as CollectionStats),
  /** KB-wide chunk browsing (Phase 1) — optional doc_id narrowing. */
  collectionChunks: (collectionId: string, docId: string | undefined, page = 1, pageSize = 50) =>
    viaAdminProxy((baseUrl) =>
      listCollectionChunksV1CollectionsCollectionIdChunksGet({
        path: { collection_id: collectionId },
        query: { page, page_size: pageSize, ...(docId ? { doc_id: docId } : {}) },
        ...THROW,
        baseUrl,
      }),
    ).then((r) => r.data as unknown as ChunkList),
  /** Env-configured runtime constants (Phase 1) — the KB Settings tab's
   * chunking preview data source. */
  systemSettings: () =>
    systemSettingsV1SystemSettingsGet(THROW).then((r) => r.data as unknown as SystemSettings),
  /** Single-collection retrieval test (Phase 3 Tab 3) — pinned POST /v1/search. */
  search: (body: {
    query: string;
    collection?: string;
    collections?: string[];
    top_k?: number;
    rerank?: boolean;
  }) =>
    viaAdminProxy(async (baseUrl) => {
      const { searchV1SearchPost } = await import("@/lib/client/sdk.gen");
      return searchV1SearchPost({ body, ...THROW, baseUrl });
    }).then((r) => r.data as unknown as SearchHitRow[]),
};
