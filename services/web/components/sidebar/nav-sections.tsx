"use client";

/** Sidebar sections — WeKnora's dual-zone nav: "Knowledge & Retrieval" and
 * "Management & System", section-labeled groups over the preserved routes.
 * The one-shot section-label reveal (Phase 6 animation 3) rides a CSS
 * animation on mount, gated behind prefers-reduced-motion. */

import { cn } from "cn";
import Link from "next/link";
import { usePathname } from "next/navigation";

export interface NavItem {
  href: string;
  label: string;
}

export const NAV_SECTIONS: { label: string; items: NavItem[] }[] = [
  {
    label: "Knowledge & Retrieval",
    items: [
      { href: "/collections", label: "Knowledge Bases" },
      { href: "/documents", label: "Documents" },
      { href: "/playground", label: "Search & Retrieval" },
    ],
  },
  {
    label: "Management & System",
    items: [
      { href: "/", label: "Dashboard" },
      { href: "/models", label: "Models & Providers" },
      { href: "/pipeline", label: "Pipeline & Queues" },
      { href: "/keys", label: "API Keys" },
      { href: "/usage", label: "Usage" },
      { href: "/logs", label: "Audit Logs & Events" },
      { href: "/settings", label: "Settings" },
    ],
  },
];

export function NavSections() {
  const pathname = usePathname() ?? "/";
  return (
    <nav aria-label="primary" className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto pt-2">
      {NAV_SECTIONS.map((section) => (
        <div key={section.label} className="nav-section-reveal">
          <p className="mb-1 px-2 text-[10.5px] font-semibold tracking-[0.06em] text-rail-text/70 uppercase max-[900px]:hidden">
            {section.label}
          </p>
          <ul className="m-0 list-none p-0">
            {section.items.map((item) => {
              const active = item.href === "/" ? pathname === "/" : pathname.startsWith(item.href);
              return (
                <li key={item.href}>
                  <Link
                    href={item.href}
                    aria-current={active ? "page" : undefined}
                    className={cn(
                      "mb-0.5 flex min-h-[32px] items-center rounded px-3 py-1.5 text-[13.5px] font-medium text-rail-text transition-all duration-300 hover:bg-white/10 hover:text-ink",
                      "hover:scale-[1.02]",
                      active && "bg-paper-deep text-ink shadow-[inset_0_0_0_1px_var(--sheet-edge)]",
                    )}
                  >
                    {item.label}
                  </Link>
                </li>
              );
            })}
          </ul>
        </div>
      ))}
    </nav>
  );
}
