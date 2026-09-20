"use client";

/** Admin login — a job ticket on the press room floor. Keeps the shake
 * animation on failure and the 429 rate-limit message. */

import { cn } from "cn";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

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
  const shakeClass = shake > 0 ? "animate-shake" : "";

  return (
    <div className="flex min-h-screen items-center justify-center bg-rail p-6">
      <Card
        key={shake}
        className={cn("w-full max-w-[380px] shadow-[0_12px_40px_-10px_#0000008c]", shakeClass)}
      >
        <CardContent className="flex flex-col gap-4 px-7 py-8">
          <div className="flex items-center gap-2.5">
            {/* biome-ignore lint/performance/noImgElement: tiny static brand mark, next/image adds nothing */}
            <img
              src="/lybrix-mark.png"
              alt=""
              width={36}
              height={36}
              className="shrink-0 rounded-[7px]"
            />
            <span className="block leading-[1.15]">
              <span className="block font-serif text-[21px] font-semibold tracking-[-0.01em] text-ink">
                lybrix
              </span>
              <small className="mt-0.5 block text-[11px] text-muted-foreground">
                ingestion &amp; retrieval
              </small>
            </span>
          </div>
          <p className="mt-0 mb-0 text-[13px] text-muted-foreground">
            Sign in to the admin console
          </p>
          <form onSubmit={submit} className="flex flex-col gap-3.5">
            <div className="flex flex-col gap-1.5">
              <Label
                htmlFor="login-username"
                className="text-xs font-semibold text-muted-foreground"
              >
                Username
              </Label>
              <Input
                id="login-username"
                autoComplete="username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                required
              />
            </div>
            <div className="flex flex-col gap-1.5">
              <Label
                htmlFor="login-password"
                className="text-xs font-semibold text-muted-foreground"
              >
                Password
              </Label>
              <Input
                id="login-password"
                type="password"
                autoComplete="current-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                required
              />
            </div>
            {error && (
              <p className="mt-0 mb-0 text-[12.5px] text-redink" role="alert">
                {error}
              </p>
            )}
            <Button type="submit" className="mt-1 w-full" disabled={busy}>
              {busy ? "Signing in…" : "Sign in"}
            </Button>
          </form>
        </CardContent>
      </Card>
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
