/** Admin session JWT (HS256, 24h) — shared by middleware and route handlers. */
import { jwtVerify, SignJWT } from "jose";

export const SESSION_COOKIE = "rag_admin_session";
export const SESSION_MAX_AGE = 24 * 60 * 60; // seconds

function secret(): Uint8Array {
  // Runtime env; falls back so `next build` and dev work without config.
  // .env.example warns to set a real AUTH_JWT_SECRET in deployment.
  const s = process.env.AUTH_JWT_SECRET ?? "dev-insecure-change-me";
  return new TextEncoder().encode(s);
}

export async function signSession(username: string): Promise<string> {
  return new SignJWT({ sub: username })
    .setProtectedHeader({ alg: "HS256" })
    .setIssuedAt()
    .setExpirationTime("24h")
    .sign(secret());
}

export async function verifySession(token: string | undefined): Promise<string | null> {
  if (!token) return null;
  try {
    const { payload } = await jwtVerify(token, secret());
    return typeof payload.sub === "string" ? payload.sub : null;
  } catch {
    return null;
  }
}
