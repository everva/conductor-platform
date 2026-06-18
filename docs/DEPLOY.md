# Deployment Guide

How to build, run, and operate the conductor-platform daemon. This complements
[`README.md`](../README.md) (architecture + the core principle) and the decision
log in [`docs/decisions/README.md`](decisions/README.md); it does not repeat them.

Every build/test/run task has a one-command [`Makefile`](../Makefile) target —
`make help` lists them. The commands below are exactly what those targets run.

> **No secret lives in this repo.** The performer command (`claude -p`) carries a
> subscription session, not a key; the `gh`-token for git auth is supplied at
> runtime. Never commit either, and never bake them into the image.

---

## 1. Build & test (the green gate)

Go **1.26.2** (see [`go.mod`](../go.mod); module `github.com/everva/conductor-platform`).

```sh
make gate          # build + test + vet + lint + fmt-check — the canonical "is it green"
```

`make gate` mirrors [`.github/workflows`](../.github/workflows/) exactly:

```sh
go build ./... && go test ./... && go vet ./... && golangci-lint run   # + gofmt -l .
```

Individual pieces: `make build` (binaries → `bin/`), `make test`, `make vet`,
`make lint`, `make fmt` / `make fmt-check`, and `make e2e` (the hermetic
`-tags e2e` end-to-end run that drives a throwaway git repo with a shell
performer — no network, no key).

---

## 2. Run locally (binary)

The daemon (`cmd/conductor`) runs `conductor.Tick` for one project, once or on an
interval. Every flag falls back to an environment variable.

```sh
make build
./bin/conductor -project myproj -root /tmp/ws -once          # single tick (smoke/CI)
./bin/conductor -project myproj -root /tmp/ws -interval 30s  # loop

make run PROJECT=myproj ROOT=/tmp/ws                         # same, via the Makefile
```

`SIGINT`/`SIGTERM` cancel the loop so the in-flight tick finishes before exit.

### Daemon flags / env

| Flag | Env | Default | Meaning |
|------|-----|---------|---------|
| `-project` | `CONDUCTOR_PROJECT` | *(required)* | project id to tick |
| `-root` | `CONDUCTOR_ROOT` | *(required)* | workspace root for clones/worktrees |
| `-dsn` | `CONDUCTOR_DSN` | *(empty)* | Postgres DSN; **empty = in-memory store** (dev/test). Never logged |
| `-base` | `CONDUCTOR_BASE_BRANCH` | `develop` | integration base branch |
| `-develop-cmd` | `CONDUCTOR_DEVELOP_CMD` | `claude -p` | performer command (space-split argv, no shell, **no key**) |
| `-host` | `CONDUCTOR_HOST_ID` | hostname | host id recorded on leases |
| `-interval` | `CONDUCTOR_INTERVAL` | `30s` | gap between ticks in loop mode |
| `-timeout` | `CONDUCTOR_TIMEOUT` | `30m` | per-develop subprocess timeout |
| `-once` | `CONDUCTOR_ONCE` | `false` | run a single tick and exit (0 on success) — CI |
| `-max-concurrent` | `CONDUCTOR_MAX_CONCURRENT` | `1` | must be `1` in Faz-1a (one active task per repo) |
| `-global-cap` | `CONDUCTOR_GLOBAL_CAP` | governor default | governor: max concurrent tasks across all projects |
| `-load-ceiling` | `CONDUCTOR_LOAD_CEILING` | governor default | governor: normalized 1-min load ceiling above which work is denied |
| `-governance` | `CONDUCTOR_GOVERNANCE` | `true` | risk-layered merge policy: T3/T4 + untiered HELD for a human; `false` = auto-merge all |
| `-http-addr` | `CONDUCTOR_HTTP_ADDR` | *(empty)* | OPTIONAL health server, e.g. `:8080`; empty = disabled |
| `-heartbeat` | `CONDUCTOR_HEARTBEAT` | *(empty)* | liveness heartbeat path written each tick; empty = disabled |
| `-heartbeat-stale` | `CONDUCTOR_HEARTBEAT_STALE` | `5m` | `-check`: age past which the daemon is STALE |
| `-heartbeat-progress` | `CONDUCTOR_HEARTBEAT_PROGRESS` | `0` | `-check`: if >0, a fresh-but-stuck heartbeat is STALE |
| `-check` | `CONDUCTOR_CHECK` | `false` | run the INDEPENDENT stall detector over `-heartbeat` and exit; does not start the daemon |

> Without `-dsn`/`CONDUCTOR_DSN` the daemon uses the **in-memory** store — fine
> for dev and tests, but state does not persist across restarts and is not shared
> with `conductorctl` in a separate process. Set a DSN for the real, shared store.

### Independent stall detector (ADR-0016)

A separate launchd/cron job runs the out-of-process backstop. It only reads the
heartbeat; it never builds the daemon. Exit 0 = FRESH, non-zero = STALE/MISSING.

```sh
make check HEARTBEAT=/var/run/conductor/hb.json
# = ./bin/conductor -check -heartbeat /var/run/conductor/hb.json
```

---

## 3. Run with Docker

A multi-stage [`Dockerfile`](../Dockerfile) builds static `conductor` +
`conductorctl` into a small alpine runtime (`git` + `ca-certificates`,
**non-root**). No secret is baked in.

```sh
make docker-build           # docker build -t conductor-platform:latest .
make compose-up             # docker compose up -d  — daemon + Postgres
docker compose logs -f conductor   # watch "tick start" / "tick done"
make compose-down           # docker compose down -v  — tear down + drop volumes
```

[`docker-compose.yml`](../docker-compose.yml) starts:

- **postgres** (`postgres:16-alpine`) — the shared store. The compose password is
  a **dev-only default** (`conductor`/`conductor`); override it for any real
  deployment.
- **conductor** — the daemon, pointed at that Postgres (it migrates on start, see
  §4) and ticking every interval. With no onboarded tasks each tick cleanly
  no-ops — that is the expected success state for a bare stack.

The daemon's health/observability HTTP server is enabled in compose on
**port 8080** (`CONDUCTOR_HTTP_ADDR=:8080`):

| Endpoint | Meaning |
|----------|---------|
| `/healthz` | liveness — 200 while the loop runs; does **not** touch the DB (a Postgres blip never flaps it) |
| `/readyz` | readiness — probes the store (cheap `ListProjects`) |
| `/status` | last-tick snapshot + backend name (`memory`/`postgres`) — never the DSN |

```sh
curl localhost:8080/healthz
curl localhost:8080/status
```

---

## 4. Postgres setup & migrations

**The daemon migrates Postgres on start.** When a non-empty DSN is configured,
`cmd/conductor` opens the store and runs the embedded goose migrations
([`internal/statestore/migrations/`](../internal/statestore/migrations/)) before
the first tick; `conductorctl` does the same when given a `-dsn`. A fresh database
is schema-ready with no separate migrate step — **there is no standalone migrate
binary**; migration happens via the daemon (or `conductorctl`) on connect.

Point either binary at the same DSN to share state:

```sh
export CONDUCTOR_DSN='postgres://USER:PASS@HOST:5432/conductor?sslmode=require'
./bin/conductor -project myproj -root /tmp/ws       # migrates, then ticks
```

### Local dev Postgres + integration tests

The DB-gated integration tests in `internal/statestore` and `internal/events`
**skip** unless `TEST_DATABASE_URL` is set, so `make test` / `make gate` stay
green DB-free. To run them against a throwaway local Postgres:

```sh
make db-up            # start postgres:16-alpine on :5433, prints TEST_DATABASE_URL
make itest            # run the DB-gated tests with TEST_DATABASE_URL
make db-down          # stop + remove the container
```

`db-up`/`db-down`/`itest`/`docker-build`/`compose-*` require Docker. `itest`
needs a reachable `TEST_DATABASE_URL` (provided by `db-up`, or your own).

---

## 5. What an operator must provide for REAL task execution

Out of the box the stack only **no-ops** ticks (it wires up and proves liveness).
To actually execute tasks, supply — all at **runtime**, never committed:

1. **A real performer command** via `CONDUCTOR_DEVELOP_CMD` (e.g. `claude -p`).
   It is a subscription session, not an API key; it is split on spaces into an
   argv and run as a subprocess (no shell).
2. **A `gh`-token** for the provisioner's git credential helper, so clones and
   per-task branch pushes are writable (ADR-0017; the read-only deploy-key trap
   is forbidden). Provide it via the environment / a mounted credential helper —
   never in the image.
3. **A Postgres DSN** (`CONDUCTOR_DSN`) for the shared, persistent store.

---

## 6. Operator flow

Drive the same store the daemon reads (use the **same `-dsn`** so a separate
`conductorctl` process is visible to the running daemon):

```sh
export CONDUCTOR_DSN='postgres://USER:PASS@HOST:5432/conductor?sslmode=require'

conductorctl onboard --base develop <repo>                  # register a project (idempotent)
conductorctl intake  --project <id> --file scenario.yaml    # ingest a scenario into the ledger
conductorctl status  --project <id> [--json]                # inspect the ledger
# the running daemon picks up the ready task on its next tick

conductorctl pause   --project <id>                         # next tick is a clean no-op
conductorctl resume  --project <id>                         # resume ticking
```

`pause`/`resume` are durable (written through the store), so a pause set by
`conductorctl` is honored by the separate daemon process (ADR-0011 §4, ADR-0020).

---

## See also

- [`README.md`](../README.md) — architecture, the one core principle, repo layout.
- [`docs/decisions/README.md`](decisions/README.md) — the decision log (ADR index,
  vision, phase plan) — the source of truth.
