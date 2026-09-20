/** Dashboard (PRD §8.1 screen 1): counters, queue depth, worker strip.
 *
 * Server shell for first paint + a client child polling live via
 * useHealth()/useQueues()/usePipeline(). Counts come from /v1/system/pipeline
 * (full-table aggregate + full doc-state breakdown), NOT from the ?limit=200
 * documents list — the old version counted states over the first 200 rows
 * only, so "ready" disagreed with the pipeline page (74 vs 93) and silently
 * stopped growing once >200 docs existed.
 */

import { DashboardClient } from "@/components/dashboard-client";

export const dynamic = "force-dynamic";

export default function DashboardPage() {
  return <DashboardClient />;
}
