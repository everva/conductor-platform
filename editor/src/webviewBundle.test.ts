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
});
