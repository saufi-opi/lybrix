"use client";

/** Highlights the current nav item via usePathname. */

import Link from "next/link";
import { usePathname } from "next/navigation";

export function ActiveNav({
  items,
}: {
  items: { href: string; label: string }[];
}) {
  const pathname = usePathname() ?? "/";
  return (
    <>
      {items.map((item) => {
        const active =
          item.href === "/" ? pathname === "/" : pathname.startsWith(item.href);
        return (
          <Link
            key={item.href}
            href={item.href}
            className={active ? "active" : undefined}
          >
            {item.label}
          </Link>
        );
      })}
    </>
  );
}
