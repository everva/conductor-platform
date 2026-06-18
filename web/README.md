# Conductor Cockpit (`web/`)

The human cockpit for the Conductor Platform — React + Vite + TypeScript (strict),
talking to the `conductor-api` gateway (ADR-0025) over typed REST + WebSocket.
This directory is a **fully isolated** frontend toolchain: the Go module never
sees it (`go build ./...` ignores `web/`; `node_modules` is git-ignored).

Decisions: `../docs/decisions/0026-frontend-stack.md` (this stack),
`../docs/decisions/0025-api-gateway.md` (the gateway surface).

## Develop

```sh
npm install
npm run dev          # vite dev server on http://localhost:5173
```

Point the cockpit at a gateway with `VITE_API_BASE` (default: same-origin):

```sh
VITE_API_BASE=http://localhost:8080 npm run dev
```

On first load you get the **token gate**: paste the gateway bearer token
(`CONDUCTOR_API_TOKEN`). It is verified with a `GET /status` probe and, on
success, stored in **sessionStorage** (tab-scoped; cleared on tab close — never
localStorage, never logged). "Sign out" clears it.

## Frontend gate (deterministic — the merge-authority check)

```sh
npm run gate         # typecheck + lint + test, in sequence
```

- `npm run typecheck` → `tsc -b` (strict, 0 errors) across app / node-config / e2e.
- `npm run lint` → `eslint . --max-warnings 0` (TS + react-hooks; zero warnings).
- `npm run test` → `vitest run` (jsdom; client, TokenGate, useEventStream).

This mirrors the Go gate's philosophy: exit-code truth, no subjective score.

## Regenerating event types (drift-free)

Event TS types come from the Go single source via the N-9 generator; the output
(`src/types/events.gen.ts`) is committed and marked **DO NOT EDIT**. Regenerate
after a taxonomy change (run from `web/`):

```sh
npm run gen:events   # cd .. && go run ./cmd/eventgen -out web/src/types/events.gen.ts
```

The file is byte-identical to `internal/events/types.gen.ts` (same generator).

## End-to-end (Playwright) — separate from the gate

The e2e smoke needs a downloaded browser, so it is **not** part of the 3B-0
must-pass gate (tsc+eslint+vitest). It joins CI in 3C. To run it locally:

```sh
npx playwright install chromium    # one-time browser download
npm run e2e                        # builds + `vite preview`, runs e2e/smoke.spec.ts
```

The smoke spec asserts the token gate renders against the built app.

## Build

```sh
npm run build        # tsc -b && vite build → dist/
npm run preview      # serve the built app
```
