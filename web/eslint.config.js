import js from "@eslint/js";
import globals from "globals";
import reactHooks from "eslint-plugin-react-hooks";
import reactRefresh from "eslint-plugin-react-refresh";
import tseslint from "typescript-eslint";

// Flat ESLint config for the cockpit: TS recommended + react-hooks rules. The
// gate runs `eslint . --max-warnings 0`, so any warning fails CI. The generated
// events.gen.ts (DO NOT EDIT) and build/report artifacts are ignored.
export default tseslint.config(
  {
    ignores: [
      "dist",
      "src/types/events.gen.ts",
      "playwright-report",
      "test-results",
    ],
  },
  {
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    files: ["**/*.{ts,tsx}"],
    languageOptions: {
      ecmaVersion: 2022,
      globals: { ...globals.browser },
    },
    plugins: {
      "react-hooks": reactHooks,
      "react-refresh": reactRefresh,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      "react-refresh/only-export-components": [
        "warn",
        { allowConstantExport: true },
      ],
    },
  },
  {
    // Node-side config/e2e files: allow node globals (process, etc.) and drop the
    // react-refresh rule (these are not React component modules).
    files: ["*.config.{ts,js}", "playwright.config.ts", "e2e/**/*.ts"],
    languageOptions: {
      globals: { ...globals.node },
    },
  },
);
