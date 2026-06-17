# Builder — light conductor for conductor-platform (ADR-0019)

This is the **build tool** that autonomously codes the conductor-platform Go
product, one Faz-1a task at a time. It is **not** the product (the product is the
Go code under `cmd/` and `internal/`). It is a bash+python light-conductor
**ported from xirigo's proven autonomous-conductor** so we don't reinvent the
macOS launchd / liveness mine-field — and so ADR-0015 (macOS runtime) and
ADR-0016 (liveness/recovery) get **live-tested for free** while it builds.

> Dogfooding paradox (PHASE-1-PLAN D7): the product can't build itself before it
> exists, so we use the separate, proven xirigo pattern. When conductor-platform
> matures it replaces this builder with itself (real dogfood).

## How it works (one tick)

```
launchd (every 5 min) ──fires──▶ conductor-tick.sh
  mkdir-lock (atomic) ─ stale-steal ─ orphan-sweep ─ caffeinate ─ explicit PATH ─ oauth-token
  └─ pick ONE ready task from the ledger (~/.conductor-platform-builder/state.json)
       deps all done? lease free? (single-host max_workers=1)
  └─ spawn ONE performer: claude -p <conductor-prompt.txt + scenario YAML>   (subscription, no API key)
       pipeline runs INSIDE the performer (ADR-0014): architect→test-first→dev→self-heal
       →GO GATE (go build && go test && go vet && golangci-lint) →commit [task:<id>]
  └─ parse the performer's Verdict JSON (deterministic, ADR-0014):
       pass            → task done
       fail/blocked    → retry_count++ ; ≥2 → blocked, else back to ready
       malformed/empty → blocked: malformed-verdict / no-verdict   (NEVER false-green)
       "Not logged in" → write AUTH_EXPIRED, STOP the tick, notify   (no kickstart into a login wall)
  └─ progress-aware watchdog kills only a genuinely wedged tick (mtime of stream + go cache)

launchd (every 3 min) ──fires──▶ auto-reconcile.py   (INDEPENDENT job, no LLM — ADR-0016)
  commit-exists-but-not-done → done  ·  dead tick + stale heartbeat → kickstart
  orphan tick/performer → kill        ·  stale/dead-owner lease → drop
```

State split mirrors xirigo's `blueprint/`(versioned) ↔ `~/.xirigo`(runtime):

- **Versioned, in this repo** (`tools/builder/`): the scripts, plists, prompt,
  scenario+holdout YAML templates, and the **template** ledger under `state/`.
- **Runtime, machine-local** (`~/.conductor-platform-builder/`): the live ledger
  `state.json`, `leases.json`, `journal/`, `conductor.lock`, logs, `oauth-token`,
  `AUTH_EXPIRED` flag. Seeded from the template on first tick.

## Files

| File | Role |
|---|---|
| `conductor-tick.sh` | One headless tick: lock/watchdog/orphan-sweep + pick task + spawn performer + parse Verdict + update ledger. |
| `auto-reconcile.py` | Independent deterministic backstop: dead-tick kickstart, stale-lease drop, commit→done reconcile. |
| `write-heartbeat.py` | Writes `heartbeat.json` each tick (for an external stall-alert; TCC-safe). |
| `conductor-prompt.txt` | Performer instruction: fill frozen Go contracts, TDD, pass the Go gate, return Verdict JSON, then commit. |
| `com.conductor-platform-builder.plist` | launchd job for the tick (RunAtLoad + KeepAlive + StartInterval, explicit PATH, FDA-scoped binary). |
| `com.conductor-platform-builder.reconcile.plist` | launchd job for the reconciler (separate job — ADR-0016). |
| `setup-oauth.sh` | Mints the long-lived OAuth token to `~/.conductor-platform-builder/oauth-token` (0600). |
| `state/` | Versioned **template** ledger (`state.json`, `leases.json`, `journal/`). Faz-1a JSON, no Postgres (ADR-0013). |
| `scenarios/` | Hand-written scenario + holdout YAML (PRE-0 contracts freeze, A-1 in-memory statestore, ...). |

## Ledger schema (Faz-1a, no Postgres — ADR-0013)

`state.json`:
```jsonc
{ "tasks": [ {
  "id": "A-1", "lane": "statestore", "tier": "T1",
  "status": "todo|ready|running|done|blocked",
  "deps": ["PRE-0"],                 // pickable only when all deps done
  "scenario_ref": "A-1-...yaml",     // relative to scenarios/
  "retry_count": 0,                  // caps at 2 -> blocked (ADR-0004)
  "last_verdict": null, "commit_sha": null, "blocked_reason": null
} ] }
```
`leases.json`: `{ "active": [{task, pid, started_at}], "max_workers": 1, "max_per_repo": 1 }`.

---

## ACTIVATION (human-manual — nothing here installs or runs itself)

> The builder paths are written for the user `atakan` at
> `/Users/atakan/Documents/GitHub/conductor-platform`. If your username or repo
> location differs, edit the absolute paths in both `.plist` files first.

### (a) Mint the OAuth token (interactive Terminal, once)
```bash
cd /Users/atakan/Documents/GitHub/conductor-platform/tools/builder
chmod +x setup-oauth.sh conductor-tick.sh
./setup-oauth.sh        # opens browser, approve, paste sk-ant-oat... token
```
Produces `~/.conductor-platform-builder/oauth-token` (0600). The keychain is
unreachable from launchd (ADR-0015); this token is how ticks authenticate.

### (b) Full Disk Access — FDA-scoped binary (ADR-0015)
launchd children need FDA to read/write the repo under `~/Documents`. Do **not**
grant FDA to the system `/bin/bash` (affects every script). Make a dedicated copy
and grant FDA to *that*:
```bash
cp /bin/bash ~/.conductor-platform-builder/builder-bash
```
Then: **System Settings → Privacy & Security → Full Disk Access → +** and add
`~/.conductor-platform-builder/builder-bash` (Cmd-Shift-G to type the path; it is
a hidden dir). Toggle it ON. Both plists invoke this binary, so they inherit the
grant. (Until this is done, ticks will fail to read the repo.)

### (c) Install + load the launchd jobs
```bash
cp com.conductor-platform-builder.plist            ~/Library/LaunchAgents/
cp com.conductor-platform-builder.reconcile.plist  ~/Library/LaunchAgents/
launchctl load -w ~/Library/LaunchAgents/com.conductor-platform-builder.plist
launchctl load -w ~/Library/LaunchAgents/com.conductor-platform-builder.reconcile.plist
```
Unload with `launchctl unload ...` to stop. For a graceful pause without
unloading, `touch ~/.conductor-platform-builder/PAUSE` (a running tick finishes;
no new tick starts). Resume: `rm` the flag.

### (d) Preflight — confirm the toolchain is reachable
The tick runs its own preflight (claude/go required; gh/golangci-lint warned),
but verify manually once:
```bash
claude -p "say ok" --dangerously-skip-permissions   # subscription, returns ok
go version
gh auth status
golangci-lint --version
```
Then watch the first tick:
```bash
tail -f ~/.conductor-platform-builder/conductor-tick.log
tail -f ~/.conductor-platform-builder/auto-reconcile.log
```

### After PRE-0 (the contracts-freeze task) lands
PRE-0 freezes the shared Go contracts and **requires human sign-off** (ADR-0019).
When PRE-0 reports done, **review the contract diff yourself**, confirm the frozen
signatures, then flip `A-1` (and later siblings) from `todo` → `ready` in
`~/.conductor-platform-builder/state.json`. This human gate is intentional in
Faz-1a — don't automate it away.

### Notes
- **Throwaway test-repo:** Faz-1a end-to-end validation (PHASE-1-PLAN §8) uses a
  separate private test-repo, not live optiway. The builder codes the
  conductor-platform repo itself; keep that distinct from any product-under-build.
- **Hidden holdout (ADR-0018):** `hidden_holdout_ref` in each scenario points to a
  repo-OUTSIDE store the performer never sees; it is injected only at an
  independent verify step. The store path (`store://holdouts/...`) is a
  placeholder — wire it to an actual local holdout dir before relying on the
  negative-test guarantee.
