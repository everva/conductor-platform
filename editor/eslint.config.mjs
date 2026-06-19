import js from "@eslint/js";
import globals from "globals";
import tseslint from "typescript-eslint";

// Flat ESLint config for the Conductor VS Code extension. TWO worlds, two global sets:
//
//   * HOST (src/**, *.ts) — the extension host is a Node CommonJS program: node globals,
//     NO react plugins.
//   * WEBVIEW (webview/**, incl. *.tsx) — the FORK cockpit React bundle (4B-3): a browser
//     program, so it gets browser globals (window, document, MessageEvent, console).
//
// Both extend js recommended + tseslint recommended (mirrors web/eslint.config.js in
// shape). No react plugin is needed — the webview entry is thin (the panels live in web/,
// linted by web's own config). The gate runs `eslint . --max-warnings 0`, so any warning
// fails. Build output (dist), the emitted electron-smoke harness (out-test), and the
// downloaded VS Code the opt-in smoke caches (.vscode-test, ~258MB — eslint would OOM
// scanning it) are ignored.
export default tseslint.config(
  {
    ignores: ["dist", "out-test", ".vscode-test"],
  },
  {
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    files: ["src/**/*.ts"],
    languageOptions: {
      ecmaVersion: 2022,
      globals: { ...globals.node },
    },
  },
  {
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    files: ["webview/**/*.ts", "webview/**/*.tsx"],
    languageOptions: {
      ecmaVersion: 2022,
      globals: { ...globals.browser },
    },
  },
);
