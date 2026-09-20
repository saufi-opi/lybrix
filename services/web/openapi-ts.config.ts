import { defineConfig } from "@hey-api/openapi-ts";

// Input is a checked-in snapshot of the control-plane API's OpenAPI schema.
// Regenerate: with a stack running, `curl http://localhost:8000/openapi.json >
// openapi.json`, or in-process via `uv run python -c` importing api.main.
// No network access happens at build time — the snapshot and generated client
// are committed to git so Docker builds stay hermetic.
export default defineConfig({
  input: "openapi.json",
  output: { path: "lib/client" },
  plugins: ["@hey-api/client-fetch", "@hey-api/typescript", "@hey-api/sdk"],
});
