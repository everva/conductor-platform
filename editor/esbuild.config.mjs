// esbuild bundler for the Conductor VS Code extension. Builds TWO bundles:
//
//   1. HOST  (dist/extension.js)   — the extension-host program: CommonJS for Node. The
//      `vscode` module is provided by the host at runtime → external (never bundled). The
//      `ws` package (4B-2 WS connector) IS bundled, but its OPTIONAL native acceleration
//      deps (bufferutil, utf-8-validate) are external so the bundle builds without them —
//      `ws` require()s them in a try/catch and runs fine in pure JS when absent.
//
//   2. WEBVIEW (dist/webview/main.js + main.css) — the FORK cockpit React bundle (4B-3).
//      A classic IIFE script the webview loads with a CSP nonce. It mounts the SHARED 3B
//      cockpit (web/src/cockpit.ts) via the `@cockpit` alias, with React + the cockpit CSS
//      BUNDLED IN. This is the cross-dir source reuse (ADR-0029): web/ is consumed as
//      source through the alias and is never modified. The webview does NO direct network
//      (host CSP `connect-src 'none'`); `import.meta.env.VITE_API_BASE` is `define`d away
//      (the host bridge owns the base URL), and React builds in production mode.
//
// Pass --watch for incremental rebuilds of BOTH bundles (optional; the gate is one-shot).
import * as esbuild from "esbuild";
import * as path from "node:path";
import { fileURLToPath } from "node:url";

const watch = process.argv.includes("--watch");
const __dirname = path.dirname(fileURLToPath(import.meta.url));

// The shared cockpit barrel, resolved AS SOURCE from web/src (web/ stays untouched).
const cockpitEntry = path.resolve(__dirname, "../web/src/cockpit.ts");

/** @type {import("esbuild").BuildOptions} */
export const hostOptions = {
  entryPoints: ["src/extension.ts"],
  outfile: "dist/extension.js",
  bundle: true,
  platform: "node",
  format: "cjs",
  target: "node18",
  external: ["vscode", "bufferutil", "utf-8-validate"],
  sourcemap: true,
  logLevel: "info",
};

/** @type {import("esbuild").BuildOptions} */
export const webviewOptions = {
  entryPoints: ["webview/main.tsx"],
  outdir: "dist/webview",
  bundle: true,
  platform: "browser",
  format: "iife",
  target: "es2022",
  jsx: "automatic",
  // @cockpit = the shared barrel as source. react / react-dom are ALIASED to the editor's
  // OWN copy so the bundle contains EXACTLY ONE React — the crux of ADR-0029 (editor brings
  // its React). The shared web/src components import "react"; WITHOUT this alias those
  // imports resolve by normal node resolution to web/node_modules whenever web/ is installed
  // (local dev), while the editor's own imports resolve to editor/node_modules → TWO React
  // copies → a dead hooks dispatcher ("Cannot read properties of null (reading 'useState')")
  // and a BLANK webview. esbuild `alias` matches "react" + its subpaths (react/jsx-runtime)
  // and "react-dom" + subpaths (react-dom/client), forcing one copy regardless of whether
  // web/ is installed. (nodePaths below is kept as a resolution root for the no-web/ CI job,
  // but it is only a FALLBACK — it did NOT dedupe when web/node_modules existed, which is why
  // a locally-built bundle was broken.)
  alias: {
    "@cockpit": cockpitEntry,
    react: path.resolve(__dirname, "node_modules/react"),
    "react-dom": path.resolve(__dirname, "node_modules/react-dom"),
  },
  nodePaths: [path.resolve(__dirname, "node_modules")],
  loader: { ".css": "css" },
  define: {
    // React production build (drops dev warnings / checks).
    "process.env.NODE_ENV": '"production"',
    // The fork doesn't use the Vite build-time API base (the host bridge owns the base
    // URL); keep the reference defined so the bundled web/src/api/client.ts builds.
    "import.meta.env.VITE_API_BASE": "undefined",
  },
  sourcemap: true,
  logLevel: "info",
};

// Run the build only when invoked directly (`node esbuild.config.mjs`), NOT when imported
// — the webview-bundle test reuses webviewOptions, and importing must have NO side effects.
const isMain =
  process.argv[1] !== undefined &&
  path.resolve(process.argv[1]) === fileURLToPath(import.meta.url);

if (isMain && watch) {
  const hostCtx = await esbuild.context(hostOptions);
  const webviewCtx = await esbuild.context(webviewOptions);
  await Promise.all([hostCtx.watch(), webviewCtx.watch()]);
  console.log("[esbuild] watching for changes (host + webview)…");
} else if (isMain) {
  // Build both; report each output's size so the gate tail shows both bundles.
  const [hostResult, webviewResult] = await Promise.all([
    esbuild.build({ ...hostOptions, metafile: true }),
    esbuild.build({ ...webviewOptions, metafile: true }),
  ]);
  logOutputs("host", hostResult);
  logOutputs("webview", webviewResult);
  console.log("[esbuild] build complete → dist/extension.js + dist/webview/main.js (+ main.css)");
}

/** Logs each emitted file + its byte size from a build's metafile. */
function logOutputs(label, result) {
  for (const [file, info] of Object.entries(result.metafile.outputs)) {
    console.log(`[esbuild] ${label}: ${file} — ${info.bytes} bytes`);
  }
}
