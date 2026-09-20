# ADR 0003 — Web UI stack and the api/web split

**Status:** Accepted · **Date:** 2026-09-21 · **Supersedes:** the "Next.js + hand-written CSS"
assumption in PRD §8 (design language retained, tooling changed)

## Context

The admin web UI (`services/web/`) shipped as a Next.js App Router app with ~1,000 lines of
hand-written CSS ("paper & press", dark-only) and no UI framework. Adding the MCP Playground
and richer interactive pages (dialogs, toasts, live polling) would mean hand-rolling every
primitive. The [FastAPI full-stack template](https://github.com/fastapi/full-stack-fastapi-template)
frontend (Vite SPA, TanStack Router/Query, shadcn/ui, generated OpenAPI client, Biome) was used
as a pattern reference.

## Decisions

1. **Next.js stays.** No Vite SPA rewrite — server components, route handlers, and the
   middleware cookie gate are load-bearing. The template is a pattern reference, not a skeleton.
2. **Tailwind CSS v4 + shadcn/ui**, re-themed to the paper & press palette. Dark-only via
   `:root` CSS variables mapped into shadcn's token names; no `.dark` block, no theme switcher,
   no next-themes. Accent discipline unchanged: press green is the only loud color; ochre,
   red ink and ledger blue are state-only.
3. **Generated TypeScript client** via `@hey-api/openapi-ts` from a **checked-in snapshot**
   (`services/web/openapi.json`) of the control-plane API's OpenAPI schema, with
   `@hey-api/client-fetch` (fetch-based — safe in server components). Regenerate with
   `npm run generate-client` after API surface changes; builds stay hermetic (no network).
   `lib/api-client.ts` remains as a thin compatibility facade over the generated SDK.
4. **TanStack Query** for server state: live polling (5s) on dashboard/pipeline/logs, focus
   refetch on lists, mutation invalidation on retry/revoke. Server components remain for first
   paint. Sonner for toasts.
5. **Biome** replaces the deprecated `next lint` (formatter + linter, `biome check .`).

## The api/web split: why not one Next.js app

Merging the control-plane REST API into the Next.js app (route handlers) was considered and
rejected:

- **Multi-consumer surface.** `/v1/*` is the scope-authed, bearer-`ragk_`-key control plane
  consumed by MCP clients, `scripts/eval`, CI checks, and curl — the web UI is just one
  consumer, and the least privileged one. Merging would make external key holders depend on
  the UI framework and its release cadence.
- **Auth model.** The api authenticates per-request API keys with scopes (`search` / `ingest` /
  `admin`) enforced server-side. The web authenticates humans with a session cookie and holds
  `API_ADMIN_KEY`/`MCP_API_KEY` only in its server-side proxies. Folding them together would
  either duplicate key auth in TypeScript or leak UI-session assumptions into the key surface.
- **Deploy and scale.** The api scales independently of the UI (2 replicas vs. 1) and rebuilds
  without a Node toolchain. A merged app would couple control-plane availability to
  `next build` and ship both on every change.
- **Cost of separation:** one extra container and one internal network hop. Worth it.

The web app's proxies (`app/api/admin/[...path]/route.ts`, `app/api/playground/route.ts`)
exist precisely so the browser never holds a bearer key — that boundary is the pattern to keep.

## Consequences

- `services/web/` gains a Node-only PostCSS/Tailwind build step; the Dockerfile stays
  `node:22-alpine` (no Python needed).
- The checked-in `openapi.json` can drift from the live api; regeneration is documented in
  CLAUDE.md and required after api schema changes.
- shadcn primitives land in `components/ui/`; domain components (shard grid, progress bar,
  retry buttons) stay as custom components on top.
