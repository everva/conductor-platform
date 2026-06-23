// Real-gateway e2e config (M3) — the cockpit driven against a REAL, freshly-built
// conductor-api (ephemeral memory store, seeded over real REST), NOT the page.route mocks
// the hermetic playwright.config.ts uses. run.sh (npm run e2e:realgw) builds + boots the
// gateway, seeds it, builds + serves the web app with VITE_API_BASE pointed at the gateway,
// and only THEN invokes this config — so here the servers are already up: no webServer, just
// point at the live preview. OPT-IN, never part of CI (CI's web job stays hermetic).
import { defineConfig, devices } from "@playwright/test";

const PORT = Number(process.env.REALGW_WEB_PORT ?? 4273);

export default defineConfig({
  testDir: "./e2e-realgw",
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  workers: 1,
  retries: 0,
  timeout: 60_000,
  expect: { timeout: 15_000 },
  reporter: "list",
  use: {
    baseURL: `http://localhost:${PORT}`,
    trace: "on-first-retry",
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
  // No webServer: run.sh pre-starts the real gateway + the web preview (it must seed the
  // gateway between the two, which a declarative webServer block can't order).
});
