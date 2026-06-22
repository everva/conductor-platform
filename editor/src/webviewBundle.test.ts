// Guards the ADR-0029 single-React invariant for the fork webview bundle.
//
// THE BUG THIS CATCHES: the webview bundles the shared web/src cockpit (via the @cockpit
// alias) which imports "react". If that import resolves to web/node_modules/react (which
// normal node resolution does whenever web/ is installed — i.e. local dev) while the
// editor's own imports resolve to editor/node_modules/react, the bundle ends up with TWO
// copies of React. Two Reacts = a null hooks dispatcher → the cockpit throws on mount
// ("Cannot read properties of null (reading 'useState')") and the Conductor panel renders
// BLANK. The rest of the gate misses this (the build still succeeds; the vitest mount tests
// mock vscode and never render the real bundle), so this test is the deterministic guard:
// it builds the real webview options with a metafile and asserts no React input comes from
// web/node_modules. The fix is the esbuild `alias` (react/react-dom → editor's copy).
import { describe, it, expect } from "vitest";
import * as esbuild from "esbuild";

describe("webview bundle (ADR-0029 single React)", () => {
  it("resolves react/react-dom to ONE copy — none from web/node_modules", async () => {
    // Dynamic import: the config is ESM (.mjs) and this test compiles as CJS. The .d.mts
    // gives it types; the .mjs main-guard means importing it does NOT trigger a build.
    const { webviewOptions } = await import("../esbuild.config.mjs");
    const result = await esbuild.build({
      ...webviewOptions,
      write: false, // in-memory; don't touch dist/
      metafile: true,
      sourcemap: false,
      logLevel: "silent",
    });
    const inputs = Object.keys(result.metafile?.inputs ?? {});

    // Sanity: React really is bundled (so the assertion below isn't vacuous).
    const reactInputs = inputs.filter((i) => /(^|\/)node_modules\/(react|react-dom)\//.test(i));
    expect(reactInputs.length).toBeGreaterThan(0);

    // The bug: a SECOND React copy pulled from web/node_modules (the alias must prevent it).
    const fromWeb = reactInputs.filter((i) => i.includes("web/node_modules"));
    expect(fromWeb, `react resolved from web/node_modules (duplicate copy): ${fromWeb.join(", ")}`).toEqual([]);
  }, 30_000);

  // Guards the N5 CSS bug: the cockpit COMPONENTS style themselves with design tokens
  // (`:root` custom properties from web/src/theme/tokens.css). The @cockpit barrel pulls in the
  // component CSS but NOT that foundation, so the fork entry (webview/main.tsx) must import it —
  // else the bundle has hundreds of `var(--…)` uses but ZERO `:root` defs and the whole cockpit
  // renders UNSTYLED in the fork. This asserts the foundation is actually bundled + emitted.
  it("bundles the design tokens (:root) so the fork cockpit is styled, not just the rules that use them (N5)", async () => {
    const { webviewOptions } = await import("../esbuild.config.mjs");
    const result = await esbuild.build({
      ...webviewOptions,
      write: false, // in-memory; inspect outputFiles, don't touch dist/
      metafile: true,
      sourcemap: false,
      logLevel: "silent",
    });

    // The token SOURCE must be an input (the fork entry imports it; ADR-0029 reuse).
    const inputs = Object.keys(result.metafile?.inputs ?? {});
    expect(
      inputs.some((i) => i.includes("web/src/theme/tokens.css")),
      "tokens.css must be bundled — the :root design tokens the cockpit's var(--…) resolve to",
    ).toBe(true);

    // …and the EMITTED css must carry the :root token DEFINITIONS, not only the component rules
    // that consume them (the bug: 736 var(--…) uses, 0 :root defs → unstyled).
    const css = result.outputFiles?.find((f) => f.path.endsWith(".css"))?.text ?? "";
    expect(css).toContain(":root");
    expect(css).toMatch(/--bg:/);
    expect(css).toMatch(/--space-2:/);
  }, 30_000);
});
