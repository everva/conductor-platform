# Dogfood: develop a repo with the product daemon

This guide shows how to use the **product** daemon (`cmd/conductor`) to autonomously
develop a repository — **including conductor-platform itself** — driving the real
loop: pick a ready task → provision a per-task worktree → **develop** with a real
`claude -p` performer → **independent deterministic verify** (the platform's gate +
a repo-external hidden holdout) → squash-merge on green with a `[task:<id>]` trailer.

It **supersedes `tools/builder/`** (the bootstrap bash light-conductor, ADR-0019,
now retired — see [`tools/builder/DEPRECATED.md`](../tools/builder/DEPRECATED.md)).
The product daemon now does what the builder did, with real binaries. For build /
run / deploy mechanics see [`DEPLOY.md`](DEPLOY.md); for the architecture and the
one core principle see [`../README.md`](../README.md).

> **The one core principle still holds.** The merge decision rides ONLY on the
> independent, deterministic verify pass (exit codes + the hidden holdout) — never
> on the performer's self-reported verdict (Rule#9). The daemon never fakes green:
> a holdout-breaking or gate-failing commit yields *changes-requested* and is
> retried, not merged.

---

## 0. Safety posture (read this first)

Self-modification is the riskiest thing the platform does, so dogfood runs under a
deliberately conservative posture:

1. **Run against a throwaway clone or a branch — never your canonical checkout.**
   Onboard the daemon against a clone of the repo. The daemon makes its own
   per-project clone under `-root/clones/<project>` and merges into **that local
   clone's** base branch only.
2. **The daemon never pushes.** `GitMerger` squash-merges the verified per-task
   branch into the local clone's base branch (`git merge --squash` + a commit with
   the `[task:<id>]` trailer) and stops there. There is **no `git push`** anywhere
   in the merge path. Pushing the integrated base branch anywhere is a separate,
   human-initiated step. For maximum safety, remove the `origin` remote from the
   working clone so a push is impossible.
3. **Human-gate risky tiers with governance ON (the default).** With
   `-governance=true` the risk-layered policy (ADR-0003) **holds T3/T4 and untiered
   tasks for a human** after a green gate (outcome `held`, no merge); only T1/T2
   auto-merge. Self-modifying tasks that touch auth, migrations, money, or core
   contracts should be tiered T3/T4 so a human reviews the green diff before it
   lands.
4. **No secrets.** The performer is a **subscription** `claude -p` (no API key). The
   gh-token for the provisioner's git credential helper and the Postgres DSN are
   supplied at runtime, never committed.

---

## 1. Onboard + intake (operator: `conductorctl`)

Point `conductorctl` and the daemon at the **same** store with the same `-dsn`
(a shared Postgres) so the operator process is visible to the running daemon.

```sh
export CONDUCTOR_DSN='postgres://USER:PASS@HOST:5432/conductor?sslmode=require'

# Onboard the repo to develop. Use a CLONE (or a fork/branch) as <repo>, not your
# canonical working copy. The project id is derived from the repo's last path
# element (e.g. /path/to/clone -> "clone").
conductorctl onboard --dsn "$CONDUCTOR_DSN" --base develop /path/to/clone

# Intake a scenario (+ its repo-external hidden holdout reference) into the ledger.
conductorctl intake --dsn "$CONDUCTOR_DSN" --project clone --file scenario.yaml

conductorctl status --dsn "$CONDUCTOR_DSN" --project clone   # CLAMP-1  util  T2  todo
```

### The scenario (rich ADR-0012 form)

A scenario is YAML. Required fields: `id`, `title`, `lane`, `tier` (one of
`T1..T4`), at least one `acceptance` criterion, and a **repo-external**
`hidden_holdout_ref` (it must NOT be a repo-relative path — the holdout body never
lives in the public checkout, ADR-0018). `deps`, `public_test_ref`,
`public_tests_outline`, and `notes` are optional.

```yaml
id: CLAMP-1
title: "internal/util: add Clamp(v, lo, hi int) int helper with a visible test"
lane: util
tier: T2                       # T1/T2 auto-merge on green; T3/T4 are held for a human
deps: []
acceptance:
  - "internal/util/clamp.go declares: func Clamp(v, lo, hi int) int."
  - "Clamp returns lo if v < lo, hi if v > hi, and v otherwise (inclusive bounds)."
  - "gofmt-clean and golangci-lint-clean; package + function doc comments."
  - "A visible test internal/util/clamp_test.go compiles and passes."
public_test_ref: internal/util/clamp_test.go
hidden_holdout_ref: "store://holdouts/CLAMP-1/spec.yaml"   # repo-EXTERNAL locator
notes: "Small, self-contained, gate-verifiable dogfood task."
```

### The hidden holdout (repo-external)

The holdout lives **outside any product repo**, under an operator-supplied root
the daemon resolves with `-holdout-store`. The layout (ADR-0018, `internal/holdout`)
is:

```
<holdout-root>/holdouts/<id>/spec.yaml          # metadata; referenced, NOT injected
<holdout-root>/holdouts/<id>/inject/<path...>   # files injected into the verify-worktree
```

`hidden_holdout_ref: "store://holdouts/<id>/spec.yaml"` resolves to the directory
`<holdout-root>/holdouts/<id>/`, and **everything under its `inject/` subdir** is
injected — at its path relative to `inject/` — into the throwaway verify-worktree.
For a Go package test the file goes at e.g.
`<holdout-root>/holdouts/CLAMP-1/inject/internal/util/clamp_holdout_test.go`, so it
lands at `internal/util/clamp_holdout_test.go` in the verify-worktree. Make it cover
the edge cases the visible test omits (below/above/at-bound, equal bounds, negative
ranges), so a thin or wrong implementation that passes the visible test still fails
the hidden gate.

---

## 2. Run the daemon with a real `claude -p` performer

```sh
conductor \
  -project clone \
  -root /path/to/workspace \                 # daemon clones + worktrees live here
  -base develop \
  -dsn "$CONDUCTOR_DSN" \
  -develop-cmd /path/to/develop.sh \         # the performer (see the wrapper note)
  -holdout-store /path/to/holdout-root \     # repo-EXTERNAL hidden holdout root
  -holdout-cmd "go test ./internal/util/" \  # how to run the injected holdout suite
  -timeout 300s \                            # per-develop subprocess bound
  -once                                      # one tick; drop for the interval loop
# governance defaults ON: T1/T2 auto-merge on green, T3/T4 + untiered are HELD.
```

### Performer: wrap `claude -p` in a single-token script

`-develop-cmd` is split on whitespace into an argv and executed **without a shell or
quoting** (`strings.Fields`). A multi-word `claude -p "<long prompt>"` therefore
**cannot** be passed inline — the prompt would be shattered into separate argv
elements. Use a single-token wrapper script as the develop command:

```sh
#!/usr/bin/env bash
# develop.sh — the daemon execs this with cwd = the per-task worktree and feeds the
# task-identity JSON (id/lane/tier/scenario_id/branch) on stdin. We invoke the real
# subscription `claude -p` (NO api key) with the task instructions as the prompt and
# grant it tool access to the worktree. claude's stdout — including the final
# single-line verdict JSON — is what the product engine parses (ADR-0014).
set -euo pipefail
PROMPT="$(cat /path/to/develop-prompt.txt)"
exec claude -p "$PROMPT" --dangerously-skip-permissions --add-dir .
```

The develop **prompt** must carry the full task instructions (what to build, where,
the gofmt/lint-clean requirement, "commit on the current branch", and "print one
single-line verdict JSON as the very last line"). The daemon passes the task
identity on stdin but **not** the acceptance criteria, so the prompt is where the
work is specified. Keep the verdict-JSON contract exactly as the engine's
`ParseVerdict` expects: the last top-level JSON object carrying a non-empty
`"result"` key (see `internal/engine/realclaude_smoke_test.go` for the proven
shape).

### What the daemon does each tick

`pick (ready task) → lease → provision worktree → develop (claude) → independent
verify → (green + auto-merge tier) squash-merge into the clone's base → mark done`.
The verify gate runs the recipe **Gates** over the performer's output, then injects
the hidden holdout into a separate throwaway verify-worktree and runs `-holdout-cmd`
there. The result is derived from **exit codes + the holdout**, never the
performer's self-report. On green and an auto-merge tier the per-task branch is
squash-merged with a `[task:<id>]` trailer; otherwise the task is held (T3/T4) or
retried (gate/holdout failure) — **no fake-green**.

Inspect / control a running daemon out-of-band (same `-dsn`):

```sh
conductorctl status  --dsn "$CONDUCTOR_DSN" --project clone
conductorctl pause   --dsn "$CONDUCTOR_DSN" --project clone   # next tick is a clean no-op
conductorctl resume  --dsn "$CONDUCTOR_DSN" --project clone
conductorctl abort   --dsn "$CONDUCTOR_DSN" --project clone   # cancel the in-flight develop
```

---

## 3. The verify gate the daemon actually runs

The daemon wires a **fixed recipe gate** of two deterministic commands, run in the
performer's worktree (see `cmd/conductor/main.go`, `conductor.Recipe`):

```
go build ./...
go test  ./...
```

plus the **hidden holdout** suite (`-holdout-cmd`, e.g. `go test ./internal/util/`)
run in a separate verify-worktree with the holdout files injected. A merge requires
**all recipe gates AND the holdout to pass**.

> **Limitation (honest).** The daemon's recipe gate is **`go build` + `go test`
> only** — it does **not** include `go vet` or `golangci-lint`, even though the
> platform's canonical gate (`make gate`, the CI gate) is
> `go build && go test && go vet && golangci-lint run`. The recipe `Gates` are
> currently hardcoded in `cmd/conductor/main.go`; there is no flag or `.conductor/`
> config to extend them. Until the daemon learns to read its gate from config, you
> can approximate a fuller gate by folding extra checks into the hidden holdout
> command, e.g. `-holdout-cmd "sh -c 'go vet ./... && golangci-lint run && go test ./internal/util/'"`
> (note: that requires a shell, so it must itself be a single-token program or a
> wrapper script, same as the performer). Making `go vet` + `golangci-lint` first-class
> recipe gates is a tracked product follow-up, not part of dogfood.

---

## 4. Gotchas when dogfooding conductor-platform ON ITSELF

Verified the hard way during the first real dogfood run. These are operator
concerns, not code changes:

- **Strip `CONDUCTOR_*` from the daemon's environment; pass config via flags.**
  The verify recipe gate runs `go test ./...` as a subprocess that **inherits the
  daemon's environment**. The platform's own `cmd/conductor` test
  (`TestParseConfig/dsn defaults empty`) asserts that with no `-dsn` the parsed DSN
  is empty — but it reads `CONDUCTOR_DSN` via env-fallback and does **not** reset
  the env. So if the daemon is launched with `CONDUCTOR_DSN` exported, that value
  leaks into the gate's `go test ./...` and the platform's **own** test fails →
  the gate honestly returns *changes-requested* and the task never merges (correct
  Rule#9 behavior, but a confusing false block). Fix: launch the daemon with the
  `CONDUCTOR_*` vars **unset in its environment**, passing them only as flags:
  ```sh
  env -u CONDUCTOR_DSN -u CONDUCTOR_PROJECT -u CONDUCTOR_ROOT \
    conductor -project clone -root /path/ws -dsn "$DSN" -develop-cmd ... -once
  ```
  (This is a real follow-up for the product: env-reading tests should use
  `t.Setenv` to isolate the env. Reported, not fixed here.)
- **One task per file-set per base.** Each squash-merge advances the clone's base
  branch. Re-running multiple tasks that touch the **same** files against the
  already-advanced base will conflict on merge; the daemon then *blocks* (no
  fake-green) rather than forcing it. Give each dogfood task a distinct file-set,
  or reset the base between independent demos.

## 5. This supersedes `tools/builder/` (ADR-0019 retirement)

`tools/builder/` was the bootstrap light-conductor that coded conductor-platform
**before the product existed** (the dogfooding paradox, ADR-0019). That job is done;
the product daemon now does it. The builder scripts are retained for reference/history
but **must not be run** — see [`tools/builder/DEPRECATED.md`](../tools/builder/DEPRECATED.md)
and the retirement note in
[`docs/decisions/0019-builder-light-conductor.md`](decisions/0019-builder-light-conductor.md).

## See also

- [`DEPLOY.md`](DEPLOY.md) — build / run / Docker / Kubernetes / Postgres + the full
  daemon flag table.
- [`../README.md`](../README.md) — architecture, the one core principle, repo layout.
- [`decisions/README.md`](decisions/README.md) — the decision log (source of truth).

---

## Appendix: a verified end-to-end dogfood run

A real run of the above against a throwaway clone of this repo (subscription
`claude -p` 2.1.181, Postgres store, governance ON):

```
tick start  project=clone
tick done   project=clone outcome=merged task=CLAMP-2
            verdict=pass review=pass merge_sha=f6c94c4…
```

- `claude` wrote `internal/util/clamp.go` (the `Clamp` helper) + `internal/util/clamp_test.go`.
- The **independent** verify gate ran `go build ./...` + `go test ./...` over the
  reviewed code **and** injected the repo-external hidden holdout
  (`clamp_holdout_test.go`, below/above/at-bound + equal-bound + negative-range
  cases) into a throwaway verify-worktree and ran it — all green (`review=pass`).
- The T2 task auto-merged into the clone's `develop` as a squash commit
  `conductor: land CLAMP-2` carrying the `[task:CLAMP-2]` trailer; the task moved
  to `done`. Nothing was pushed to any remote.

