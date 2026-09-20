/** POST /api/auth/login — bcrypt against ADMIN_PASSWORD_HASH, sets the
 * session cookie. 5 failures per IP per 60s window -> 429 (in-memory;
 * single replica). */

import bcrypt from "bcryptjs";
import { type NextRequest, NextResponse } from "next/server";
import { SESSION_COOKIE, SESSION_MAX_AGE, signSession } from "@/lib/auth";

export const runtime = "nodejs";

const WINDOW_MS = 60_000;
const MAX_FAILURES = 5;
const failures = new Map<string, { count: number; resetAt: number }>();

function clientIp(req: NextRequest): string {
  return req.headers.get("x-forwarded-for")?.split(",")[0]?.trim() ?? "local";
}

export async function POST(req: NextRequest) {
  const ip = clientIp(req);
  const now = Date.now();

  const record = failures.get(ip);
  if (record && record.count >= MAX_FAILURES && now < record.resetAt) {
    return NextResponse.json(
      { error: "too many failed attempts — try again in a minute" },
      { status: 429, headers: { "Retry-After": "60" } },
    );
  }

  const body = (await req.json().catch(() => null)) as {
    username?: string;
    password?: string;
  } | null;
  const username = body?.username ?? "";
  const password = body?.password ?? "";
  const hash = process.env.ADMIN_PASSWORD_HASH;
  const expectedUser = process.env.ADMIN_USER ?? "admin";

  const ok =
    !!hash &&
    username === expectedUser &&
    (await bcrypt.compare(password, hash).catch(() => false));

  if (!ok) {
    const rec =
      record && now < record.resetAt
        ? { count: record.count + 1, resetAt: record.resetAt }
        : { count: 1, resetAt: now + WINDOW_MS };
    failures.set(ip, rec);
    const retry = rec.count >= MAX_FAILURES ? 60 : undefined;
    return NextResponse.json(
      { error: "invalid username or password" },
      { status: 401, ...(retry ? { headers: { "Retry-After": String(retry) } } : {}) },
    );
  }

  failures.delete(ip);
  const token = await signSession(username);
  const res = NextResponse.json({ ok: true });
  res.cookies.set(SESSION_COOKIE, token, {
    httpOnly: true,
    sameSite: "lax",
    path: "/",
    maxAge: SESSION_MAX_AGE,
    // NOT secure: the compose entrypoint is plain HTTP behind traefik.
  });
  return res;
}
