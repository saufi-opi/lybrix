/** Base-URL wiring for the generated OpenAPI client (@hey-api/client-fetch).
 *
 * Server components call the control-plane API directly via API_URL; browser
 * calls go same-origin (empty base) and ride the next.config.mjs rewrite
 * `/v1/:path*` → http://api:8000. Same isBrowser logic the hand-rolled
 * lib/api-client.ts used — applied here via setConfig so regenerating
 * lib/client/ never loses it (client.gen.ts is auto-generated).
 */
import { client } from "@/lib/client/client.gen";

const isBrowser = typeof window !== "undefined";
const API_URL = process.env.API_URL ?? "http://localhost:8000";

export const apiBaseUrl = isBrowser ? "" : API_URL;

client.setConfig({ baseUrl: apiBaseUrl });
