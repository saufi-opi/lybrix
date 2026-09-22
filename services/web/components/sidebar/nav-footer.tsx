"use client";

/** Sidebar footer — user profile (session email), live system health dot,
 * MCP connection badge, sign-out. Health rides useHealth() (5s poll); the
 * dot pulses via the CSS pulse-dot keyframe (Phase 6 animation 4). */

import { cn } from "cn";
import { Plug, PlugZap } from "lucide-react";
import { LogoutButton } from "@/components/logout-button";
import { useHealth } from "@/lib/queries";

function HealthDot({ ok }: { ok: boolean }) {
  return (
    <span
      className={cn(
        "inline-block size-2 shrink-0 rounded-full",
        ok ? "animate-pulse-dot bg-press" : "bg-redink",
      )}
      aria-hidden="true"
    />
  );
}

export function NavFooter() {
  const health = useHealth();
  const ok = health.data?.status === "ok";

  return (
    <div className="mt-auto flex flex-col gap-2.5 border-t border-white/10 pt-3 max-[900px]:mt-0 max-[900px]:ml-auto max-[900px]:border-t-0 max-[900px]:pt-0 max-[900px]:pl-1.5">
      {/* system health + MCP badge rows — hidden on the mobile top-strip */}
      <div className="flex flex-col gap-1.5 px-1 max-[900px]:hidden">
        <span className="flex items-center gap-2 text-[11.5px] text-rail-text">
          <HealthDot ok={ok} />
          <span>{health.isPending ? "checking…" : ok ? "system healthy" : "degraded"}</span>
        </span>
        <span className="flex items-center gap-2 text-[11.5px] text-rail-text">
          {ok ? (
            <PlugZap className="size-3.5 text-press" aria-hidden="true" />
          ) : (
            <Plug className="size-3.5" aria-hidden="true" />
          )}
          <span>MCP · /mcp</span>
        </span>
      </div>
      <LogoutButton />
    </div>
  );
}
