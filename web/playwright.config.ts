// Playwright config for the cockpit e2e smoke (ADR-0026 frontend gate). It builds
// + serves the app via `vite preview` and runs specs under e2e/. Because it needs
// a downloaded browser (`npx playwright install chromium`), the MUST-PASS gate for
// 3B-0 is tsc + eslint + vitest (npm run gate); e2e joins CI in 3C. See
// web/README.md for the run command.
import { defineConfig, devices } from "@playwright/test";

const PORT = 4173;

export default defineConfig({
  testDir: "./e2e",
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  // The org self-hosted runner is shared/CPU-contended. Running the browser specs in
  // PARALLEL workers (default = core count) makes the contexts starve each other under
  // load, so a correct test blows the 30s timeout / "element not found" (the same specs
  // pass locally + passed on earlier runs). Serialize to ONE worker on CI so each test
  // gets the full CPU, and give generous retries + timeouts. Assertions are unchanged —
  // this is reliability headroom, not a weakened gate (no fake-green). Local stays
  // parallel + no retries for fast feedback.
  workers: process.env.CI ? 1 : undefined,
  retries: process.env.CI ? 2 : 0,
  timeout: process.env.CI ? 60_000 : 30_000,
  expect: { timeout: process.env.CI ? 15_000 : 5_000 },
  reporter: "list",
  use: {
    baseURL: `http://localhost:${PORT}`,
    trace: "on-first-retry",
    actionTimeout: process.env.CI ? 15_000 : 0,
    navigationTimeout: process.env.CI ? 30_000 : 0,
  },
  projects: [
    { name: "chromium", use: { ...devices["Desktop Chrome"] } },
  ],
  // Build then serve the static app; reuse a running server locally.
  webServer: {
    command: `npm run build && npm run preview -- --port ${PORT} --strictPort`,
    url: `http://localhost:${PORT}`,
    reuseExistingServer: !process.env.CI,
    timeout: 120_000,
  },
});
