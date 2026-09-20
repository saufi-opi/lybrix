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
  getDocumentV1DocumentsDocIdGet,
  getShardsV1DocumentsDocIdShardsGet,
  healthV1SystemHealthGet,
  listCollectionsV1CollectionsGet,
  listDocumentsV1DocumentsGet,
  listEventsV1EventsGet,
  pipelineV1SystemPipelineGet,
  queuesV1SystemQueuesGet,
  retryV1DocumentsDocIdRetryPost,
} from "@/lib/client/sdk.gen";
import type { DocumentOut, ShardOut } from "@/lib/client/types.gen";

/** Re-exported so existing imports of the hand-written types keep working. */
export type DocumentRow = DocumentOut;
export type ShardRow = ShardOut;

/** throwOnError makes the SDK reject non-2xx instead of returning the error
 * envelope — matches the old api<T>() contract of throwing on !res.ok. */
const THROW = { throwOnError: true } as const;

export const apiClient = {
  documents: (params = "") => {
    const qs = new URLSearchParams(params.replace(/^\?/, ""));
    return listDocumentsV1DocumentsGet({
      query: Object.fromEntries(qs),
      ...THROW,
    }).then((r) => r.data as DocumentRow[]);
  },
  document: (id: string) =>
    getDocumentV1DocumentsDocIdGet({ path: { doc_id: id }, ...THROW }).then(
      (r) => r.data as DocumentRow,
    ),
  shards: (id: string) =>
    getShardsV1DocumentsDocIdShardsGet({ path: { doc_id: id }, ...THROW }).then(
      (r) => r.data as ShardRow[],
    ),
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
    listCollectionsV1CollectionsGet(THROW).then((r) => r.data as Record<string, unknown>[]),
  retry: (id: string, scope: string) =>
    retryV1DocumentsDocIdRetryPost({
      path: { doc_id: id },
      body: { scope },
      ...THROW,
    }).then((r) => r.data as { retried: string }),
};
