/** Dashboard shell — WeKnora's dual-zone rail + container. Route group; URL
 * layout unchanged.
 *
 * The rail stays a simple hand-built element (208px = w-52, sticky, full-height)
 * — shadcn's Sidebar is heavier than this needs. Structure moved to
 * components/sidebar/: header (brand + workspace chip), sections (WeKnora's
 * "Knowledge & Retrieval" / "Management & System" zone labels over the
 * preserved routes), footer (health dot + MCP badge + sign-out). Upload left
 * the rail — ingestion now lives inside Knowledge Bases (quick-ingest); the
 * standalone /upload page stays reachable from the dashboard header.
 * Styling is Tailwind utilities over the paper & press tokens. */

import { DashboardHeaderActions } from "@/components/dashboard-header-actions";
import { NavFooter } from "@/components/sidebar/nav-footer";
import { NavHeader } from "@/components/sidebar/nav-header";
import { NavSections } from "@/components/sidebar/nav-sections";

export default function DashboardLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return (
    <div className="flex min-h-screen flex-row max-[900px]:flex-col">
      <nav className="sticky top-0 flex h-screen w-52 shrink-0 flex-col border-r border-rail-edge bg-rail px-3.5 py-5 max-[900px]:h-auto max-[900px]:w-full max-[900px]:flex-row max-[900px]:flex-wrap max-[900px]:items-center max-[900px]:gap-x-3 max-[900px]:px-2.5 max-[900px]:py-2">
        <NavHeader />
        <NavSections />
        <NavFooter />
      </nav>
      <main className="min-w-0 flex-1 px-8 py-7 max-[900px]:px-4">
        <div className="mx-auto flex max-w-[1240px] flex-col gap-3">
          <div className="flex items-center justify-end">
            <DashboardHeaderActions />
          </div>
          {children}
        </div>
      </main>
    </div>
  );
}
