import { defineConfig } from "@playwright/test";

// Minimal Playwright config: its mere presence marks this repo as the "web"
// stack so the scaffolder selects the visual-diff recipe (ADR-0023).
export default defineConfig({
  testDir: "./tests",
});
