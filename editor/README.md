# Conductor — VS Code extension (`editor/`)

Agent-fleet cockpit inside the editor (Faz-4, ADR-0027). This is the **built-in
webview extension** that the `everva/conductor-editor` Code-OSS fork will bundle. It
lives here (not in the fork repo) so it shares the conductor-platform frontend
toolchain and, later, the 3B cockpit components with `web/`.

## Status — 4B-0 scaffold

Installable, activatable skeleton only:

- Contributes the **Conductor** activity-bar view container.
- Registers a placeholder `conductor.connect` command (shows an info message; real
  connect/auth is **4B-1**).
- Registers a `conductor.fleet` webview view that renders a strict-CSP "not
  connected" placeholder (real host↔webview bridge is **4B-2**, cockpit panels
  reusing 3B are **4B-3**).

No gateway connection, no auth/token, no postMessage bridge yet — those are
4B-1..4B-3.

## Layout

```
editor/
  src/extension.ts        extension entry (activate/deactivate + wiring, testable)
  src/extension.test.ts   vitest unit tests (headless, vscode mocked)
  test/vscode-mock.ts     headless `vscode` mock used by the unit tests
  test/runVscodeSmoke.ts  OPT-IN @vscode/test-electron smoke (not in the gate)
  test/suite/             in-host smoke suite (real vscode)
  media/conductor.svg     activity-bar icon
  esbuild.config.mjs      bundler → dist/extension.js (CommonJS, vscode external)
  tsconfig.json           strict typecheck (noEmit; esbuild ships the bundle)
  tsconfig.test.json      compiles the electron smoke harness → out-test/
  go.mod                  config-only Go carve-out (no Go source; see below)
```

## Deterministic gate

Run from `editor/` after installing deps:

```sh
cd editor
npm install      # creates package-lock.json (committed)
npm run gate     # typecheck + lint + vitest + esbuild build
```

`gate` = `tsc --noEmit` (strict) → `eslint . --max-warnings 0` → `vitest run`
(headless; `vscode` is aliased to `test/vscode-mock.ts`) → `node esbuild.config.mjs`
(bundle `src/extension.ts` → `dist/extension.js`). All four must pass.

## Opt-in electron smoke (NOT in the gate)

`npm run test:vscode` downloads a full VS Code build and launches electron to assert
the extension activates and `conductor.connect` is registered. It needs a
display/xvfb, so it is **double-gated** and excluded from the headless CI gate (mirrors
the repo's `CP_REAL_CLAUDE` opt-in pattern):

- it is not part of `npm run gate`; and
- it **skips cleanly (exit 0)** unless `CP_VSCODE_SMOKE=1` is set.

```sh
CP_VSCODE_SMOKE=1 npm run test:vscode   # only where a display is available
```

## Go carve-out

`editor/go.mod` is a nested, **config-only** Go module (no Go source). It makes the
root module's `go build/vet/test ./...` wildcard skip `editor/` entirely, so a stray
Go file inside `editor/node_modules` can never affect the Go gate. Same pattern as
`web/go.mod` (ADR-0026/0027).
