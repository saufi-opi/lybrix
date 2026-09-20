"use client";

/** Sidebar logout: clears the session cookie, back to /login. */

import { useRouter } from "next/navigation";
import { useState } from "react";

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
    <button type="button" className="logout-btn" onClick={logout} disabled={busy}>
      {busy ? "Signing out…" : "Sign out"}
    </button>
  );
}
