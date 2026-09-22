"use client";

/** Sidebar header — brand mark, lybrix wordmark, workspace chip.
 *
 * WeKnora's dual-zone shell puts a workspace switcher above the nav sections;
 * this deployment is single-workspace, so the chip renders as a disabled
 * affordance (visual parity, no dead menu). */

import { cn } from "cn";
import { ChevronDown } from "lucide-react";

export function NavHeader() {
  return (
    <div className="flex flex-col gap-3 pb-1">
      <span className="flex items-center gap-2 px-1">
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
      {/* workspace chip — single workspace, disabled menu (WeKnora parity) */}
      <button
        type="button"
        disabled
        aria-disabled="true"
        className={cn(
          "flex min-h-[32px] cursor-not-allowed items-center justify-between gap-2 rounded border border-white/10 bg-white/5 px-2.5 py-1 text-left",
          "max-[900px]:hidden",
        )}
      >
        <span className="flex min-w-0 items-center gap-2">
          <span className="inline-flex size-4 shrink-0 items-center justify-center rounded-[3px] bg-press font-mono text-[9px] font-bold text-[#0e1511]">
            L
          </span>
          <span className="min-w-0 truncate text-[12.5px] font-medium text-ink">workspace</span>
        </span>
        <ChevronDown className="size-3.5 shrink-0 text-rail-text" />
      </button>
    </div>
  );
}
