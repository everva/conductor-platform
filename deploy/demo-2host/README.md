# Two-machine (Linux + macOS) capability-routed multi-project demo

This shows the platform's multi-host vision **live**: two daemons on two machines, one
shared Postgres, a **web** project routed to the Linux host (Playwright → visual-diff
gate) and an **iOS** project routed to the macOS host (maestro UI-flow gate), each coded
autonomously and merged only through its **own deterministic gate** — the cockpit
watching throughout. Tasks reach the right machine by **capability routing**
(`task.Requires ⊆ host.Capabilities`, ADR-0008); a repo is leased to exactly one host at
a time (one active task per repo).

There are three layers, from fully-local proof to the real two-machine run:

| Layer | Where | What it proves | LLM |
|------|-------|----------------|-----|
| **Faz A** | one machine | routing + multi-project + multi-host lease + the gate, deterministically | a deterministic sh-performer |
| **Faz B** | this runbook | how to stand up two real hosts | — |
| **Faz C** | your Linux + Mac | the full vision with real tools | real `claude -p` |

---

## Faz A — local deterministic proof (run it now)

Two artifacts, both offline and deterministic (no `claude`, no network):

1. **Committed e2e test** — the rigorous proof, runs in CI's real-PG `e2e` job:

   ```sh
   make db-up   # or any Postgres; sets TEST_DATABASE_URL
   TEST_DATABASE_URL="postgres://conductor:conductor@localhost:5433/conductor?sslmode=disable" \
     go test -tags e2e -run MultiHost -v ./internal/conductor/
   ```

   `internal/conductor/multihost_e2e_test.go` builds **two capability-distinct
   Conductors over one shared Postgres** (host-linux: `linux,web,backend` /
   host-mac: `macos,ios-build,maestro`) and asserts, on the real store:
   - **routing safety** — host-mac cannot pick a `web` task, host-linux cannot pick an
     `ios-build` task (each a clean no-op);
   - **routed merges** — host-linux merges the web task, host-mac merges the iOS task
     (multi-project, each on its capable host);
   - **gate authority** — a lying performer on the routed host is **blocked** by the
     independent gate, never fake-greened (Rule#9).

   (Registry-level routing/lease/reap is also proven deterministically, in-memory, by
   `internal/conductor/twohost_test.go`.)

2. **Live two-process run** — genuinely-separate daemon **processes** with the real
   binaries, coordinating through one shared (isolated, auto-dropped) schema:

   ```sh
   deploy/demo-2host/run-demo.sh           # needs the :5433 Postgres container, git, go
   KEEP=1 deploy/demo-2host/run-demo.sh    # retain the workdir + schema to inspect
   PG_DSN='postgres://…?sslmode=disable' PG_CONTAINER=my-pg deploy/demo-2host/run-demo.sh
   ```

   It builds `conductor` + `conductorctl`, seeds a web-shaped and an iOS-shaped throwaway
   repo, onboards + intakes them through the **real CLI**, then:
   - **Proof 1 (refusal):** `conductor -once -host host-linux -capabilities linux,web,backend`
     on the iOS project — refuses `I-1` (`ios-build ⊄` linux caps), no clone, no work.
   - **Proof 2 (routed work):** two looping daemons (`host-linux → web-shop`,
     `host-mac → ios-app`) develop → verify → merge their task.
   - **Proof 3 (git truth):** each project's `develop` tip carries its `[task:<id>]`
     trailer, on the right host's clone, with no cross-contamination.

   Expected tail:

   ```
   ✓ host-linux refused I-1 (no clone, no work) — capability routing holds
   ✓ both tasks merged
   ✓ web-shop merged on host-linux, ios-app merged on host-mac — no cross-contamination
   HOST        CAPABILITIES             HEARTBEAT
   host-linux  backend,linux,web        4s ago
   host-mac    ios-build,macos,maestro  4s ago
    DEMO PASSED — two-machine capability-routed multi-project, LIVE
   ```

### The hidden holdout (ADR-0018)

Each scenario's `hidden_holdout_ref` (`store://holdouts/<id>/…`) resolves under a
repo-**external** `-holdout-store` root. At verify time the holdout — a real acceptance
test the performer never sees — is injected into a throwaway verify-worktree and run, so
the merge rides a hidden check, not the performer's self-report.

### Why a deterministic performer here

In the nested-agent sandbox, `claude` subprocess auth is unavailable, so `-develop-cmd`
is a deterministic sh-performer that writes real Go, commits, and emits a schema-valid
Verdict — the same swap `internal/conductor/e2e_test.go` uses. **Only the LLM brain is
stubbed; the routing, multi-host lease, gate, and merge are the genuine article.** On a
real host (Faz C) `-develop-cmd` is `claude -p`.

---

## Faz B — stand up two real hosts

Both hosts run the **same** `conductor` binary against the **same** `-dsn` (a Postgres
reachable from both — e.g. the `deploy/k8s` `postgres` Service, or a managed PG). Each
host differs only in `-host`, `-capabilities`, and which project(s) it drives.

### Shared Postgres + cockpit gateway

```sh
# Postgres reachable from both hosts (or use deploy/k8s/postgres.yaml).
export CONDUCTOR_DSN='postgres://conductor:…@db.internal:5432/conductor?sslmode=require'

# The cockpit talks to the token-auth gateway (HTTP/WS), not the daemons directly.
conductor-api -dsn "$CONDUCTOR_DSN" -addr :8080   # CONDUCTOR_API_TOKEN in env
```

Point the **Conductor Editor** (or web cockpit) at `http(s)://<gateway>:8080` with the
token in VS Code SecretStorage (the token never enters the webview or logs).

### Linux host (web projects — Playwright)

```sh
# Prereqs: git, go, Node + Playwright (`npx playwright install --with-deps`),
#          a logged-in `claude` (subscription; no API key).
conductor \
  -dsn "$CONDUCTOR_DSN" \
  -host host-linux -capabilities linux,web,backend,playwright \
  -project web-shop -root /var/lib/conductor \
  -develop-cmd 'claude -p' \
  -recipe-dir /path/to/web-shop \        # .conductor/config.yaml → visual-diff gate
  -holdout-store /var/lib/conductor/holdouts \
  -governance -interval 30s
```

### macOS host (iOS projects — maestro + Xcode)

```sh
# Prereqs: git, go, Xcode + command-line tools, maestro (`maestro --version`),
#          a logged-in `claude`.
conductor \
  -dsn "$CONDUCTOR_DSN" \
  -host host-mac -capabilities macos,ios-build,maestro \
  -project ios-app -root /Users/Shared/conductor \
  -develop-cmd 'claude -p' \
  -recipe-dir /path/to/ios-app \         # .conductor/config.yaml → maestro gate (requires: ios-build)
  -holdout-store /Users/Shared/conductor/holdouts \
  -governance -interval 30s
```

The web recipe carries no hard capability (web runs anywhere); the iOS recipe declares
`requires: ios-build`, so its tasks only ever lease onto the Mac. To hard-pin a web
project to the Linux host too, give its tasks `requires: [web]` (or `playwright`) and
ensure only the Linux host advertises that capability.

> **Recipes** are produced by the scaffolder from each repo's shape
> (`internal/scaffolder`, ADR-0023): a `playwright.config.*` repo → **web** profile
> (visual-diff gate); a `maestro/` + `*.xcodeproj`/`Project.swift` repo → **iOS** profile
> (maestro gate, `requires: ios-build`). Commit the drafted `.conductor/config.yaml` and
> point the daemon at it with `-recipe-dir`.

### Independent recovery (both hosts share it)

Run the reconcile job (a `deploy/k8s` CronJob, or cron) against the **same** `-dsn` so a
dead host's lease is reaped and merged tasks are reconciled from git trailers:

```sh
conductor -reconcile -dsn "$CONDUCTOR_DSN" -lease-ttl 30m -host-stale 2m
```

---

## Faz C — the real run

With both hosts up and a real `claude` login, onboard a real web repo and a real iOS
repo, intake their scenarios, and watch from the cockpit: the web work develops and
passes its **visual-diff** gate on Linux, the iOS work develops and passes its **maestro**
gate on the Mac, and each merges only on a green deterministic gate. Same mechanism as
Faz A — the LLM and the real gates are the only additions.

## Notes / current edges

- **`task.Requires` source.** Routing reads `task.Requires`. The iOS recipe's
  `requires: ios-build` is the routing hook; today the live demo stamps it onto the task
  directly (the gateway/CLI has no `task --requires` verb yet — a documented follow-up).
  Intake's `scenario.lane` is a work-category label and is intentionally **not** mapped
  to `Requires` (lane vocabulary ≠ host-capability vocabulary), so it never silently
  strands a task on a capability-declaring host.
- **One repo, one host at a time.** The lease is repo-scoped; two hosts can run different
  projects concurrently but never the same repo at once (ADR-0008).
