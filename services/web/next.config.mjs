/** @type {import('next').NextConfig} */
// Browser-side /v1/* calls are proxied same-origin to the control-plane API.
// The destination MUST be a literal here (Next bakes rewrites into the build
// manifest — env vars in this file are evaluated at BUILD time, not runtime).
// The compose network name for the API service is `api` (internal DNS).
const API_DEST = process.env.NEXT_API_DEST ?? "http://api:8000";

const nextConfig = {
  output: "standalone",
  async rewrites() {
    return [
      {
        source: "/v1/:path*",
        destination: `${API_DEST}/v1/:path*`,
      },
    ];
  },
};

export default nextConfig;
