"use client";

/** Sidebar logout: clears the session cookie, back to /login. */

import { useRouter } from "next/navigation";
import { useState } from "react";
import { Button } from "@/components/ui/button";

export function LogoutButton() {
  const router = useRouter();
  const [busy, setBusy] = useState(false);

  async function logout() {
    setBusy(true);
    await fetch("/api/auth/logout", { method: "POST" }).catch(() => {});
    router.replace("/login");
    router.refresh();
  }

  return (
    <Button
      type="button"
      variant="outline"
      size="sm"
      className="w-full border-white/10 bg-transparent text-[12.5px] text-rail-text hover:bg-white/10 hover:text-ink"
      onClick={logout}
      disabled={busy}
    >
      {busy ? "Signing out…" : "Sign out"}
    </Button>
  );
}
