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

// gatewayProxy maps the gateway's REST + WS paths to VITE_API_TARGET so the browser stays
// same-origin (ApiClient base URL "") and the gateway needs no CORS change. Shared by the dev
// server AND `vite preview` (the latter so the OPT-IN real-gateway e2e harness — run.sh /
// playwright.realgw.config.ts — drives the built app against a real conductor-api without CORS;
// the hermetic e2e suite page.route-mocks these paths so the proxy is never exercised there).
const gatewayProxy = {
  "/projects": { target: apiTarget, changeOrigin: true },
  "/hosts": { target: apiTarget, changeOrigin: true },
  "/status": { target: apiTarget, changeOrigin: true },
  "/events": { target: apiTarget, changeOrigin: true },
  // /ws is the WebSocket event stream — needs ws:true to upgrade the connection.
  // changeOrigin is FALSE here (unlike the REST routes): the proxied handshake then
  // keeps the browser's Host (localhost:5173), which matches the Origin the browser
  // sends. The gateway's WS Accept is same-origin by default (cmd/conductor-api/
  // events.go) and 403s a cross-origin handshake — exactly what changeOrigin:true
  // causes by rewriting Host to the :8080 target while Origin stays :5173. Prod serves
  // the app + API under one ingress origin, so this only affects the dev/preview proxy.
  "/ws": { target: apiTarget, changeOrigin: false, ws: true },
};

export default defineConfig({
  plugins: [react()],
  server: { proxy: gatewayProxy },
  preview: { proxy: gatewayProxy },
});
