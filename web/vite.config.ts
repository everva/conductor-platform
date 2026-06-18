import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Vite config for the cockpit dev server + build. Vitest configuration lives in
// vitest.config.ts (separate so the vite vs vitest type surfaces don't clash).
// The Playwright e2e suite lives under e2e/ and runs separately (real browser).
//
// Dev proxy (3B-1): so `npm run dev` is CORS-free, the gateway's REST + WS paths
// are proxied to VITE_API_TARGET (default http://localhost:8080). The browser stays
// same-origin (ApiClient base URL is ""), so the gateway needs no CORS change. In
// prod the ingress routes both the static app and the API under one origin (3C).
const apiTarget = process.env.VITE_API_TARGET ?? "http://localhost:8080";

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      "/projects": { target: apiTarget, changeOrigin: true },
      "/hosts": { target: apiTarget, changeOrigin: true },
      "/status": { target: apiTarget, changeOrigin: true },
      "/events": { target: apiTarget, changeOrigin: true },
      // /ws is the WebSocket event stream — needs ws:true to upgrade the connection.
      "/ws": { target: apiTarget, changeOrigin: true, ws: true },
    },
  },
});
