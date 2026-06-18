import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Vite config for the cockpit dev server + build. Vitest configuration lives in
// vitest.config.ts (separate so the vite vs vitest type surfaces don't clash).
// The Playwright e2e suite lives under e2e/ and runs separately (real browser).
export default defineConfig({
  plugins: [react()],
});
