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
  retries: process.env.CI ? 1 : 0,
  reporter: "list",
  use: {
    baseURL: `http://localhost:${PORT}`,
    trace: "on-first-retry",
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
