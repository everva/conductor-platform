// esbuild bundler for the Conductor VS Code extension. Bundles the TS entry into a
// single CommonJS file for the extension host. `vscode` is provided by the host at
// runtime, so it is marked external (never bundled). Pass --watch for incremental
// rebuilds during development (optional; the gate runs a one-shot build).
import * as esbuild from "esbuild";

const watch = process.argv.includes("--watch");

/** @type {import("esbuild").BuildOptions} */
const options = {
  entryPoints: ["src/extension.ts"],
  outfile: "dist/extension.js",
  bundle: true,
  platform: "node",
  format: "cjs",
  target: "node18",
  external: ["vscode"],
  sourcemap: true,
  logLevel: "info",
};

if (watch) {
  const ctx = await esbuild.context(options);
  await ctx.watch();
  console.log("[esbuild] watching for changes…");
} else {
  await esbuild.build(options);
  console.log("[esbuild] build complete → dist/extension.js");
}
