import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// Vitest configuration (kept separate from vite.config.ts so the vite/vitest
// type surfaces stay isolated). jsdom env + a setup file that wires
// @testing-library/jest-dom matchers; e2e/ is Playwright's and excluded.
export default defineConfig({
  plugins: [react()],
  test: {
    globals: true,
    environment: "jsdom",
    setupFiles: ["./vitest.setup.ts"],
    exclude: ["e2e/**", "node_modules/**", "dist/**"],
  },
});
