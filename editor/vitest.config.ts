import { defineConfig } from "vitest/config";
import { fileURLToPath } from "node:url";

// Vitest configuration for the extension's deterministic, headless unit tests. The
// extension host is Node (environment: "node"). The real `vscode` module only exists
// inside a running VS Code/electron host, so unit tests alias `vscode` to a local
// mock (test/vscode-mock.ts) and assert the extension's registration calls + webview
// HTML against it — no electron, no display. The OPT-IN electron smoke (test/) is
// excluded here; it is compiled+run separately via `npm run test:vscode`.
export default defineConfig({
  resolve: {
    alias: {
      vscode: fileURLToPath(new URL("./test/vscode-mock.ts", import.meta.url)),
    },
  },
  test: {
    globals: true,
    environment: "node",
    include: ["src/**/*.test.ts"],
    exclude: ["test/**", "node_modules/**", "dist/**", "out-test/**"],
  },
});
