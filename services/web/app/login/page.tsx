"use client";

/** Admin login — 9router-style centered card on the dark warm ground. */

import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";

function LoginForm() {
  const router = useRouter();
  const params = useSearchParams();
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [shake, setShake] = useState(0);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const res = await fetch("/api/auth/login", {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ username, password }),
      });
      if (res.ok) {
        // Validate the redirect target — only same-origin absolute paths.
        const next = params.get("next") ?? "/";
        router.replace(next.startsWith("/") ? next : "/");
        router.refresh();
        return;
      }
      const body = (await res.json().catch(() => null)) as { error?: string } | null;
      setError(
        res.status === 429
          ? "Too many attempts — wait a minute and try again."
          : (body?.error ?? "Login failed"),
      );
      setShake((n) => n + 1);
    } catch {
      setError("Network error — is the API up?");
      setShake((n) => n + 1);
    } finally {
      setBusy(false);
    }
  }

  // re-trigger the shake animation by remounting the card on each failure
  const shakeClass = shake > 0 ? "shake" : "";

  return (
    <div className="login-wrap">
      <div className={`login-card ${shakeClass}`} key={shake}>
        <div className="login-brand">
          <span className="brand-mark">rag-platform</span>
          <small>ingestion &amp; retrieval</small>
        </div>
        <p className="login-sub">Sign in to the admin console</p>
        <form onSubmit={submit}>
          <label className="field">
            <span>Username</span>
            <input
              autoComplete="username"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              required
            />
          </label>
          <label className="field">
            <span>Password</span>
            <input
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              required
            />
          </label>
          {error && (
            <p className="login-error" role="alert">
              {error}
            </p>
          )}
          <button className="primary login-btn" type="submit" disabled={busy}>
            {busy ? "Signing in…" : "Sign in"}
          </button>
        </form>
        <p className="login-foot muted">
          Credentials are configured on the server (ADMIN_USER / ADMIN_PASSWORD_HASH).
        </p>
      </div>
    </div>
  );
}

export default function LoginPage() {
  return (
    <Suspense fallback={null}>
      <LoginForm />
    </Suspense>
  );
}
