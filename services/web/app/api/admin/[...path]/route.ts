/** Same-origin admin proxy: the browser talks to /api/admin/v1/*, this
 * handler attaches Authorization: Bearer $API_ADMIN_KEY and forwards to the
 * control-plane API. The key never reaches the client; /v1/* GETs that are
 * keyless upstream keep flowing through the next.config rewrite instead. */
import { NextRequest, NextResponse } from "next/server";
import { SESSION_COOKIE, verifySession } from "@/lib/auth";

export const runtime = "nodejs";

const API_URL = process.env.API_URL ?? "http://api:8000";

async function forward(req: NextRequest, method: "GET" | "POST") {
  const user = await verifySession(req.cookies.get(SESSION_COOKIE)?.value);
  if (!user) {
    return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  }
  const suffix = req.nextUrl.pathname.slice("/api/admin".length); // "/v1/keys"
  const upstream = await fetch(`${API_URL}${suffix}${req.nextUrl.search}`, {
    method,
    headers: {
      "content-type": "application/json",
      authorization: `Bearer ${process.env.API_ADMIN_KEY ?? ""}`,
    },
    body: method === "POST" ? await req.text() : undefined,
    cache: "no-store",
  });
  return new NextResponse(await upstream.text(), {
    status: upstream.status,
    headers: {
      "content-type": upstream.headers.get("content-type") ?? "application/json",
    },
  });
}

export async function GET(req: NextRequest) {
  return forward(req, "GET");
}

export async function POST(req: NextRequest) {
  return forward(req, "POST");
}
