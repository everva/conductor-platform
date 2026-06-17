# Conductor Platform

A generic, multi-project autonomous coding **"Kontaktör"** platform. You distill a
task into a *scenario + holdout* by talking to the assistant; the platform then
picks that task up across one or more projects and drives it through a **standard
quality gate**, so every output is "quality-gate-approved." The anchor is the
**xirigo pattern** (Conductor–Performer–Ledger with a **deterministic** quality
gate) — explicitly **not** a subjective LLM-judge.

## The one core principle

> **You can only guarantee quality with DETERMINISTIC evidence. A subjective
> LLM score can NEVER be the quality gate.**

An LLM-judge merge-gate is non-deterministic and was abandoned as unworkable;
deterministic exit codes (build / test / lint / holdout) plus a fresh-eyes review
are what hold up. In this platform the LLM is strictly an **advisor, never the
merge gate**. The merge decision rides only on the independent, deterministic
verify pass — never on the performer's self-reported verdict (this is "Rule#9":
do not trust a component's self-report; verify it independently).

See [`docs/decisions/README.md`](docs/decisions/README.md) for the full decision
log (vision, this core principle, the ADR table, and the phase plan).

## Architecture

The platform is a Go monorepo. One **tick** is one fresh task, orchestrated by
`internal/conductor`:

1. **Pick a ready task** from the ledger — `internal/registry` (`PickReady`)
   on top of the frozen `StateStore`.
2. **Acquire the repo lease** — one active task per repo (ADR-0008).
3. **Provision a workspace** — `internal/provisioner` makes one isolated
   per-project clone, then cuts a fresh per-task `git worktree` on a short-lived
   per-task branch from the project's base branch (ADR-0017). Auth is a writable
   `gh`-token credential helper; the read-only deploy-key trap is forbidden.
4. **Develop** — `internal/engine` runs the recipe's develop command as a
   subprocess. The single generic **`CommandEngine`** runs the in-performer
   pipeline (typically `claude -p`); a new task type is a new recipe, not new Go
   code. It returns an evidence-based `Verdict`.
5. **Independent verify gate** — `internal/verify` runs the deterministic recipe
   gates (build / test / vet / lint) over the performer's output and injects the
   **repo-external hidden holdout** into a *separate, throwaway verify-worktree*
   (ADR-0018). The result is derived from exit codes, **not** from the
   performer's self-reported `Verdict.Result`. It never fakes green: a
   holdout-breaking commit yields *changes-requested*.
6. **Squash-merge** — only on an independent pass, the per-task branch is
   squash-merged into the base branch with a `[task:<id>]` git trailer (ADR-0004);
   then the task is marked done. The lease is released and the worktree cleaned up
   regardless of outcome.

Supporting pieces:

- **`internal/statestore`** — the **frozen** `StateStore` interface: the single
  persistence seam for projects, tasks, leases, and scenarios. Faz-1a ships an
  in-memory implementation; Postgres is a later task (ADR-0010/0013). Observed
  runtime truth (did a branch merge) is *derived from git each tick*, never cached.
- **`internal/engine`** — the **FROZEN 5-verb `EngineAdapter`**:
  `Develop`, `Verify`, `Health`, `Events`, `Control` (ADR-0002). Plus the
  evidence-based `Verdict`/`Check`/`ReviewResult` types and the LLM-resilience
  sentinel errors (`ErrAuthExpired`, `ErrMalformedVerdict`, `ErrNoVerdict`),
  which map directly onto ADR-0014. Malformed / missing verdicts → *blocked*,
  never fake-green.
- **`internal/reconcile`** — an independent, **deterministic** recovery backstop
  with NO LLM: a stale-lease reaper plus a git-trailer-derived task reconcile,
  designed to run as a *separate job* so the conductor can recover from its own
  death (ADR-0016). It matches the `[task:<id>]` trailer exactly (never fuzzy).

Decision records for every component live in
[`docs/decisions/`](docs/decisions/).

## Status

- **Faz-1a core: COMPLETE (9/9) and verified end-to-end.** Eight packages —
  `internal/{statestore,registry,engine,provisioner,verify,reconcile,conductor}`
  plus `cmd/conductorctl` — with all gates green
  (`go build` / `go test` / `go vet` / `golangci-lint`). The end-to-end test
  `internal/conductor/e2e_test.go` exercises onboard → develop → verify → merge
  **and** the negative path (a holdout-breaking task is blocked; a malformed
  verdict is blocked — no fake-green).
- **`cmd/conductor` daemon (N-1): done** — a runnable tick-loop binary with
  `-once`, graceful shutdown, and structured logging.
- **Faz-1b: in progress.** Planned/pending work includes the Postgres
  `StateStore`, the resource-governor (parallelism), intake, the scaffolder, and
  events — see the night plan and the phase plan below.

Precision notes (do not overclaim):

- The proven end-to-end test currently uses a shell performer; a **real
  `claude -p` end-to-end** run against a throwaway repo is a tracked Faz-1b item
  (N-6).
- The **Postgres `StateStore` is still pending** (N-4). Both `cmd/conductor` and
  `cmd/conductorctl` wire the **in-memory** store today; they are independent
  binaries and do not yet share a backing store (that arrives with the Postgres
  swap).

Source of truth for status, the ADR index, and the phase plan:
[`docs/decisions/README.md`](docs/decisions/README.md),
[`docs/PHASE-1-PLAN.md`](docs/PHASE-1-PLAN.md), and
[`docs/NIGHT-AUTONOMOUS-PLAN.md`](docs/NIGHT-AUTONOMOUS-PLAN.md).

## Build & test

Go **1.26.2** (see [`go.mod`](go.mod); module `github.com/everva/conductor-platform`).

The full quality gate is:

```sh
go build ./... && go test ./... && go vet ./... && golangci-lint run
```

## Run

The platform uses the **subscription `claude -p`** performer — there is **no API
key**. The develop command is an operator-supplied command string; no key or
token is read or embedded by these binaries.

### `conductor` — the tick daemon

Runs `conductor.Tick` for a project, once or on an interval. Flags (each falls
back to an environment variable):

| Flag | Env | Default | Meaning |
|------|-----|---------|---------|
| `-project` | `CONDUCTOR_PROJECT` | *(required)* | project id to tick |
| `-root` | `CONDUCTOR_ROOT` | *(required)* | workspace root for clones/worktrees |
| `-base` | `CONDUCTOR_BASE_BRANCH` | `develop` | integration base branch |
| `-develop-cmd` | `CONDUCTOR_DEVELOP_CMD` | `claude -p` | performer command (space-split argv, no shell) |
| `-host` | `CONDUCTOR_HOST_ID` | hostname | host id recorded on leases |
| `-interval` | `CONDUCTOR_INTERVAL` | `30s` | gap between ticks in loop mode |
| `-timeout` | `CONDUCTOR_TIMEOUT` | `30m` | per-develop subprocess timeout |
| `-once` | `CONDUCTOR_ONCE` | `false` | run a single tick and exit (exit 0 on success) — used in CI |
| `-max-concurrent` | `CONDUCTOR_MAX_CONCURRENT` | `1` | must be `1` in Faz-1a (one active task per repo) |

```sh
# single tick (CI / smoke)
go run ./cmd/conductor -project myproj -root /tmp/ws -once

# loop with the subscription performer
go run ./cmd/conductor -project myproj -root /tmp/ws -interval 30s -develop-cmd "claude -p"
```

`SIGINT`/`SIGTERM` cancel the loop so the in-flight tick finishes before exit.

### `conductorctl` — the operator client

A thin CLI that drives the same `StateStore` (Faz-1a: in-memory). Subcommands:

```sh
conductorctl onboard [--base <branch>] <repo>          # register a project (idempotent)
conductorctl intake  --project <id> --file <scenario.yaml>   # ingest a scenario into the ledger
conductorctl status  --project <id> [--json]           # print the ledger summary
conductorctl pause   --project <id>                    # pause a project's loop
conductorctl resume  --project <id>                    # resume a project's loop
```

## Repository layout

```
conductor-platform/
├── cmd/
│   ├── conductor/      # tick daemon: loop / -once tick runner + graceful shutdown
│   └── conductorctl/   # operator CLI: onboard / intake / status / pause / resume
├── internal/
│   ├── statestore/     # FROZEN StateStore interface + in-memory impl (ADR-0010/0013)
│   ├── registry/       # task selection, leases, lifecycle on the StateStore (ADR-0004/0008)
│   ├── engine/         # FROZEN 5-verb EngineAdapter + generic CommandEngine (ADR-0002/0014)
│   ├── provisioner/    # per-project clone + per-task worktree, gh-token auth (ADR-0017)
│   ├── verify/         # independent deterministic gate + holdout injection (ADR-0003/0018)
│   ├── reconcile/      # deterministic stale-lease reaper + git-trailer reconcile (ADR-0016)
│   └── conductor/      # tick orchestration spine + e2e_test.go (ADR-0001)
├── docs/
│   └── decisions/      # ADRs + decision-log README (source of truth)
└── tools/
    └── builder/        # bootstrap light-conductor that autonomously codes this repo (ADR-0019)
```

`tools/builder/` is the **bootstrap light-conductor**: a separate tool (ported
from the xirigo pattern) that autonomously codes the conductor-platform itself
until the product is mature enough to dogfood. It is not the product.

## Decisions

The decision log is the **source of truth** for the vision, the core principle,
every architectural decision (ADR-0001 … ADR-0019), and the phase plan:

- [`docs/decisions/README.md`](docs/decisions/README.md) — anchor + ADR index
- [`docs/PHASE-1-PLAN.md`](docs/PHASE-1-PLAN.md) — Faz-1 build plan
- [`docs/NIGHT-AUTONOMOUS-PLAN.md`](docs/NIGHT-AUTONOMOUS-PLAN.md) — current work list
