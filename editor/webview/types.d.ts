// Ambient declarations for the FORK webview bundle (Faz-4 DALGA 4B, 4B-3).
//
// The webview tsconfig (tsconfig.webview.json) typechecks `webview/**` while resolving
// the `@cockpit` alias to `web/src/cockpit.ts` and following its transitive imports. The
// web/src tree assumes a Vite/browser toolchain that the bare TS compiler doesn't know
// about, so we mirror the few build-time globals web/ declares (web/src/vite-env.d.ts)
// plus the VS Code webview api. These are TYPE-ONLY shims: at runtime esbuild bundles the
// real React + CSS and `define`s away `import.meta.env` (see esbuild.config.mjs). No web/
// file is touched — we only re-declare what web/ already declares for its own build.

// web/src/*.tsx import their CSS as a side-effecting module (e.g. `import "./fleet.css"`).
// esbuild bundles these via the css loader; for tsc they're opaque modules.
declare module "*.css";

// web/src/api/client.ts reads `import.meta.env.VITE_API_BASE` (Vite build-time env).
// Mirror web/src/vite-env.d.ts so the alias-followed source typechecks. The fork doesn't
// use it (the host bridge owns the base URL); esbuild defines it as `undefined`.
interface ImportMetaEnv {
  readonly VITE_API_BASE?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}

// The VS Code webview api, injected into the webview's global scope by the host. It is the
// ONLY channel the fork webview has to the extension (CSP `connect-src 'none'` forbids any
// direct network) — every REST/WS call rides this postMessage seam (4B-2). No token ever
// crosses it; auth is host-side (ADR-0027).
declare function acquireVsCodeApi(): { postMessage(msg: unknown): void };
