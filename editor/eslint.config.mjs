import js from "@eslint/js";
import globals from "globals";
import tseslint from "typescript-eslint";

// Flat ESLint config for the Conductor VS Code extension. The extension host is a
// Node CommonJS program (NOT a browser/React app), so this uses node globals and
// has NO react plugins. Mirrors web/eslint.config.js in shape: js recommended +
// tseslint recommended. The gate runs `eslint . --max-warnings 0`, so any warning
// fails. Build output (dist) and the emitted electron-smoke harness (out-test) are
// ignored.
export default tseslint.config(
  {
    ignores: ["dist", "out-test"],
  },
  {
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    files: ["**/*.ts"],
    languageOptions: {
      ecmaVersion: 2022,
      globals: { ...globals.node },
    },
  },
);
