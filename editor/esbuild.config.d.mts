// Type surface for the (JS) esbuild config, so TS consumers (the webview-bundle
// regression test) can import the build options. The runtime values live in
// esbuild.config.mjs; this only declares their types.
import type { BuildOptions } from "esbuild";

export const hostOptions: BuildOptions;
export const webviewOptions: BuildOptions;
