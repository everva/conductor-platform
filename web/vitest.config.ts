import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// Vitest configuration (kept separate from vite.config.ts so the vite/vitest
// type surfaces stay isolated). jsdom env + a setup file that wires
// @testing-library/jest-dom matchers; e2e/ + e2e-realgw/ are Playwright's and excluded.
export default defineConfig({
  plugins: [react()],
  test: {
    globals: true,
    environment: "jsdom",
    setupFiles: ["./vitest.setup.ts"],
    exclude: ["e2e/**", "e2e-realgw/**", "node_modules/**", "dist/**"],
    // The org self-hosted runner is shared; under CPU contention jsdom renders +
    // user-event interactions that take <1s locally have intermittently blown the
    // default 5000ms per-test timeout (uniform "Test timed out in 5000ms" across the
    // interaction suites, while the same code passed on prior runs). Give generous
    // headroom so a loaded runner doesn't fail a correct test — assertions are
    // unchanged; a genuinely hung test still fails, just later. (userEvent delays are
    // also disabled at each setup() — see the *.test.tsx files.)
    testTimeout: 20000,
    hookTimeout: 20000,
  },
});
