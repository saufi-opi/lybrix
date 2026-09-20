"use client";

/** Highlights the current nav item via usePathname. The active item is a
 * paper chip stamped onto the ink rail. */

import { cn } from "cn";
import Link from "next/link";
import { usePathname } from "next/navigation";

export function ActiveNav({ items }: { items: { href: string; label: string }[] }) {
  const pathname = usePathname() ?? "/";
  return (
    <>
      {items.map((item) => {
        const active = item.href === "/" ? pathname === "/" : pathname.startsWith(item.href);
        return (
          <Link
            key={item.href}
            href={item.href}
            className={cn(
              "mb-0.5 flex items-center rounded px-3 py-1.5 text-[13.5px] font-medium text-rail-text transition-colors duration-100 hover:bg-white/10 hover:text-ink",
              active && "bg-paper-deep text-ink shadow-[inset_0_0_0_1px_var(--sheet-edge)]",
            )}
          >
            {item.label}
          </Link>
        );
      })}
    </>
  );
}
