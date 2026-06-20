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

`npm run test:vscode` downloads a full VS Code build and launches electron to assert, in a
REAL VS Code host, that the extension activates, `conductor.connect` + `conductor.showDiff`
are registered, and the **native diff render** works — opening a `conductor-diff:` URI routes
to the extension's content provider and the `.diff` suffix resolves to the `diff` language
(the 4C-1b live-render proof the unit tests can only mock). It needs a display/xvfb, so it is
**double-gated** and excluded from the headless CI gate (mirrors the repo's `CP_REAL_CLAUDE`
opt-in pattern):

- it is not part of `npm run gate`; and
- it **skips cleanly (exit 0)** unless `CP_VSCODE_SMOKE=1` is set.

```sh
CP_VSCODE_SMOKE=1 npm run test:vscode   # only where a display is available
```

> If it fails with `bad option: --no-sandbox` (every flag rejected, exit 9), your shell has
> `ELECTRON_RUN_AS_NODE=1` set, which makes the VS Code binary run as plain Node and reject the
> GUI flags. Clear it for the run:
> `env -u ELECTRON_RUN_AS_NODE CP_VSCODE_SMOKE=1 npm run test:vscode`.

## Live cockpit check (manual — needs a VS Code window)

The smoke above proves activation + the native diff render automatically. To eyeball the
**webview cockpit** (Fleet / Events / Intake) painting live data over the postMessage bridge,
bring up a gateway (commands below are verified against an in-memory store; use `-dsn` for a
store a separate daemon can share):

```sh
export CONDUCTOR_API_TOKEN="$(openssl rand -hex 24)"     # ≥16 chars, not the placeholder
go run ./cmd/conductor-api -addr :8099 &                 # serves /readyz /status /projects /ws /events
# onboard a project so the Fleet panel has data:
curl -s -H "Authorization: Bearer $CONDUCTOR_API_TOKEN" \
  -d '{"repo":"everva/demo-app","base_branch":"main"}' http://localhost:8099/projects
```

Then open the `editor/` folder in VS Code and press **F5** ("Run Extension") to launch an
Extension Development Host (or `code --extensionDevelopmentPath="$PWD/editor" --new-window`).
In that window: **Cmd+Shift+P → "Conductor: Connect to Gateway"**, URL `http://localhost:8099`,
token = your `CONDUCTOR_API_TOKEN`. The activity-bar **Conductor** view's Fleet panel shows
`demo-app` and the status bar shows the link state. A `$(git-compare) Conductor: N diff(s)`
item appears when the daemon emits a green-gate diff (run the tick daemon on the project, on a
shared `-dsn`); click it (or run **"Conductor: Show Task Diff"**) to open the native read-only
diff. The token lives only in SecretStorage + the gateway request/WS URL — never the webview.

## Live e2e check (4E) — editor observes the REAL loop

The checks above prove the cockpit + native diff in isolation. **4E** proves the editor
observes the genuine autonomous loop end to end. Two opt-in, env-gated proofs (excluded from
every gate, mirroring `CP_REAL_CLAUDE`):

**Backend — real `claude -p` drives the full pipeline to a merge + a `KindDiff`** (Go, 4E-1):

```sh
# real subscription claude writes code, the independent gate verifies it, it merges, and the
# bounded KindDiff is emitted — all REAL, only the LLM is no longer a stub:
CP_REAL_CLAUDE=1 CP_CLAUDE_BIN="$(command -v claude)" \
  go test -tags "e2e realclaude" -run RealClaude -v ./internal/conductor/
```

**Editor — the host DiffObserver receives a LIVE `KindDiff` over the real WS** (TS, 4E-2).
Stand up the production topology (daemon + gateway sharing a Postgres bus), then run the
env-gated live test against it:

```sh
make db-up                                                # Postgres on :5433 (shared bus)
export DSN="postgres://conductor:conductor@localhost:5433/conductor?sslmode=disable"
export CONDUCTOR_API_TOKEN="$(openssl rand -hex 24)"
go run ./cmd/conductor-api -dsn "$DSN" -addr :8080 &      # gateway over the SHARED bus

# a throwaway product repo + onboard + intake a task (any scheme-valid hidden_holdout_ref;
# the daemon resolves store:// via -holdout-store) — see internal/conductor/e2e_test.go for
# the repo/scenario/holdout shapes. Then, with the editor test already connected:
CP_LIVE_GATEWAY=ws://localhost:8080 CP_LIVE_TOKEN="$CONDUCTOR_API_TOKEN" \
  npx vitest run src/diffObserver.live.test.ts &          # connects, waits for a live diff
go run ./cmd/conductor -project <id> -root <dir> -dsn "$DSN" \
  -develop-cmd <performer> -holdout-store <dir> -once     # green gate → KindDiff on the bus
```

The live test resolves on the first `diff` frame and asserts it is a real, token-free diff
(non-empty patch, the changed file present, the bearer token absent).

**Editor — distill (G3) + approve→merge (G4) are now committed env-gated tests too.** Against the
same running stack:

```sh
# G3 — the host bridge proxies POST /distill to the gateway and gets PROPOSED scenarios back.
# Requires the gateway host to have an authed subscription `claude` (its distiller shells out to
# `claude -p`); discovers a project via GET /projects. An http(s):// or ws(s):// base both work.
CP_LIVE_GATEWAY=http://localhost:8080 CP_LIVE_TOKEN="$CONDUCTOR_API_TOKEN" \
  npx vitest run src/hostBridge.live.test.ts

# G4 — ControlClient approves a HELD task and the daemon merges it (it leaves awaiting-approval).
# Precondition: CP_LIVE_PROJECT has exactly one task parked in awaiting-approval (a held T3/T4)
# and the daemon runs over the shared DSN so it merges after approval.
CP_LIVE_GATEWAY=http://localhost:8080 CP_LIVE_TOKEN="$CONDUCTOR_API_TOKEN" CP_LIVE_PROJECT=<id> \
  npx vitest run src/controlClient.live.test.ts
```

The token rides only the `Authorization` header / WS `?token=` query — never the webview; each
live test also asserts the token never appears in any host→webview message or returned result.

> Verification level: the **diff (G1), distill (G3), and approve→merge (G4) checks are now
> automated, committed env-gated tests** (`diffObserver.live.test.ts`, `hostBridge.live.test.ts`,
> `controlClient.live.test.ts`) — collected-but-skipped without their env, so the default gate stays
> deterministic and offline. They require a live stack to actually run (G3 also needs an authed
> `claude -p` on the gateway host; G4 needs a held task + the running daemon), exactly like the
> original manual checks — now reproducible on demand. Only the `GET /events?kind=diff` history
> backfill remains a manual runbook verification.

## Go carve-out

`editor/go.mod` is a nested, **config-only** Go module (no Go source). It makes the
root module's `go build/vet/test ./...` wildcard skip `editor/` entirely, so a stray
Go file inside `editor/node_modules` can never affect the Go gate. Same pattern as
`web/go.mod` (ADR-0026/0027).
