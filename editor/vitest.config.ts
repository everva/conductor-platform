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
    // Host src tests + the FORK-MODE webview wiring test (connect.ts is React-free and
    // alias-free, so it runs headlessly here; the React render is verified by the webview
    // typecheck + esbuild build per ADR-0029, NOT a vitest render).
    include: ["src/**/*.test.ts", "webview/**/*.test.ts"],
    exclude: ["test/**", "node_modules/**", "dist/**", "out-test/**"],
  },
});
