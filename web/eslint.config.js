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
  {
    // Dev-only harness entries (the UI primitive gallery) define + mount their own
    // components inline like a bootstrap entry; they are not part of the HMR-able
    // app tree, so the react-refresh "must export components" rule doesn't apply.
    files: ["src/dev/**/*.{ts,tsx}"],
    rules: {
      "react-refresh/only-export-components": "off",
    },
  },
  {
    // Boundary lock (ADR-0028): the shared cockpit UI surface (exposed via
    // src/cockpit.ts and consumed by both the web App and the 4B fork webview) must
    // stay web-bootstrap-agnostic. Forbid these modules from importing auth/session/
    // main so the host can inject transport + token and the boundary can't rot. They
    // already do NOT import these; this just freezes that.
    files: [
      "src/fleet/**/*.{ts,tsx}",
      "src/events/**/*.{ts,tsx}",
      "src/intake/**/*.{ts,tsx}",
      "src/api/**/*.{ts,tsx}",
      "src/ui/**/*.{ts,tsx}",
    ],
    rules: {
      "no-restricted-imports": ["error", {
        patterns: [{
          group: ["**/auth/*", "**/auth/**", "**/main", "**/main.tsx"],
          message: "Shared cockpit UI must stay web-bootstrap-agnostic (no auth/session/main) — the host injects transport + token. See ADR-0028.",
        }],
      }],
    },
  },
);
