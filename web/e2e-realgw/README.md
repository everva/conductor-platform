# Real-gateway e2e harness (M3)

The cockpit's **end-to-end verification against a REAL gateway + real data** — the answer to
"yapılan işler Playwright ile kontrol edilmeli; hangi veritabanına bağlanıyor, hangi veriyle?".

It is the counterpart to the hermetic Playwright suite (`web/e2e/`, run by CI), which drives the
cockpit against `page.route` **mocks** — fast + deterministic, but never a real gateway or DB. This
harness instead boots a **real, freshly-built `conductor-api`** and drives the real app against it.

## What it does (`npm run e2e:realgw`)

`e2e-realgw/run.sh` orchestrates, in order:

1. `go build ./cmd/conductor-api` — the real gateway binary.
2. Boot it on `:8899` with an **ephemeral in-memory store** (no `-dsn`) + a throwaway token.
3. **Seed real data over the real REST API**: onboard `everva/e2e-demo` → intake `seed.yaml`
   (task `E2E-RUN`, deterministic, claude-free) → lease it via the agent API. The lease flips the
   task's persisted status to `running` (M1). The script **fails loudly** if the seed didn't take.
4. Build the web app (same-origin) and serve it via `vite preview` on `:4273`, with the gateway
   paths proxied to `:8899` (the `preview.proxy` in `vite.config.ts`) — so the browser is CORS-free
   and the gateway sees a same-origin WS handshake, exactly like the prod single-origin ingress.
5. Run `playwright.realgw.config.ts` (specs in this folder) against the live app.

## Which DB / which data

- **DB:** an **ephemeral in-memory store** inside the throwaway gateway process. **Never** a real
  Postgres, **never** PROD, **never** the live `conductor`/`optiway` data. Nothing here can touch
  prod — it builds and boots its own gateway and tears it down on exit.
- **Data:** deterministic, seeded here over real REST — project `everva/e2e-demo`, task `E2E-RUN`
  (`e2e-realgw/seed.yaml`), leased to host `e2e-host`.

## What it proves

The M1 status-consistency fix **end-to-end through the real gateway**: leasing flips the persisted
status to `running`, `GET /projects/{id}/tasks` (the editor sessions tree's exact data source)
reports it, and the board shows the task in the **Running** column with its real lease host — no
mocks. Screenshot: `test-results/realgw-board-running.png`.

## Why it is opt-in (not CI)

CI's `web cockpit` job is intentionally **hermetic** (no gateway / Postgres / claude), so this
harness is opt-in — the same pattern as the `CP_REAL_CLAUDE` real-claude paths and the
`CP_VSCODE_SMOKE` editor electron smoke. Run it locally to verify the real round-trip; the GERÇEK-PG
gateway proof of the same flip runs in the Go real-PG gate (`TestAgentLeaseStatusFlipPG`).

## The full M3 verification map

| Layer | Where | DB / data | In CI? |
| --- | --- | --- | --- |
| Gateway flip (unit) | `cmd/conductor-api/agent_test.go` | in-memory | ✅ Go job |
| Gateway flip (real DB) | `cmd/conductor-api/onboard_pg_test.go` `TestAgentLeaseStatusFlipPG` | GERÇEK-PG (skips w/o `TEST_DATABASE_URL`) | local real-PG gate |
| Editor tree renders status | `editor/src/sessionsTree.test.ts` | pure | ✅ editor job |
| Board buckets status (mocked) | `web/e2e/board.spec.ts` | `page.route` fixtures | ✅ web job |
| **Board + /tasks (real gateway)** | **`web/e2e-realgw/`** | **ephemeral gateway, seeded REST** | **opt-in (`npm run e2e:realgw`)** |
