/** GET /api/auth/status — {authenticated, hasPassword} for the login page. */
import { type NextRequest, NextResponse } from "next/server";
import { SESSION_COOKIE, verifySession } from "@/lib/auth";

export const runtime = "nodejs";

export async function GET(req: NextRequest) {
  const user = await verifySession(req.cookies.get(SESSION_COOKIE)?.value);
  return NextResponse.json({
    authenticated: user !== null,
    hasPassword: !!process.env.ADMIN_PASSWORD_HASH,
  });
}
