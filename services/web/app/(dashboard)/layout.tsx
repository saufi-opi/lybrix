/** Dashboard shell — the ink rail + container. Route group; URL layout unchanged.
 *
 * The rail stays a simple hand-built element (208px = w-52, sticky, full-height)
 * — shadcn's Sidebar is heavier than this needs. Styling is Tailwind utilities
 * over the paper & press tokens: bg-rail, rail-edge border, paper chip for
 * the active nav item. */

import { ActiveNav } from "@/components/active-nav";
import { LogoutButton } from "@/components/logout-button";

const NAV = [
  { href: "/", label: "Dashboard" },
  { href: "/pipeline", label: "Pipeline" },
  { href: "/documents", label: "Documents" },
  { href: "/upload", label: "Upload" },
  { href: "/playground", label: "Playground" },
  { href: "/logs", label: "Logs" },
  { href: "/collections", label: "Collections" },
  { href: "/models", label: "Models" },
  { href: "/keys", label: "API Keys" },
  { href: "/usage", label: "Usage" },
  { href: "/settings", label: "Settings" },
];

export default function DashboardLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return (
    <div className="flex min-h-screen flex-row max-[900px]:flex-col">
      <nav className="sticky top-0 flex h-screen w-52 shrink-0 flex-col border-r border-rail-edge bg-rail px-3.5 py-5 max-[900px]:h-auto max-[900px]:w-full max-[900px]:flex-row max-[900px]:flex-wrap max-[900px]:items-center max-[900px]:px-2.5 max-[900px]:py-2">
        <span className="flex items-center gap-2 pb-4 pl-2 pr-2 max-[900px]:py-0 max-[900px]:px-1.5">
          {/* biome-ignore lint/performance/noImgElement: tiny static brand mark, next/image adds nothing */}
          <img
            src="/lybrix-mark.png"
            alt=""
            width={30}
            height={30}
            className="block shrink-0 rounded-md"
          />
          <span className="block leading-[1.15]">
            <span className="block font-serif text-lg font-semibold tracking-[-0.01em] text-press">
              lybrix
            </span>
            <small className="block text-[10.5px] font-medium tracking-[0.01em] text-rail-text">
              ingestion &amp; retrieval
            </small>
          </span>
        </span>
        <ActiveNav items={NAV} />
        <div className="mt-auto border-t border-white/10 pt-3 max-[900px]:mt-0 max-[900px]:ml-auto max-[900px]:border-t-0 max-[900px]:pt-0 max-[900px]:pl-1.5">
          <LogoutButton />
        </div>
      </nav>
      <main className="min-w-0 flex-1 max-w-[1240px] px-8 py-7">{children}</main>
    </div>
  );
}
