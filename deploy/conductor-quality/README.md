# Conductor Quality Kit

Reproducible, **server-independent**, **capability-gated** install of the whole Conductor
quality layer. Everything the fleet needs to produce honest, verified work — the
deterministic gates, the i18n merge-driver, the semantic auditor loop, and the
real-backend E2E + visual runner — lives here in version control instead of as loose
files on one machine.

**Why:** previously all of this was hand-placed under `~/conductor-agent/` on a single
host (davinci). Format that box and it was gone. Now: a formatted / re-provisioned host
is one `install.sh` away from full recovery, and a **new** host only declares what it
_can do_ (capabilities) and gets exactly those features.

## Install / recover

```bash
# on the host (after cloning conductor-platform)
cd deploy/conductor-quality
./install.sh                       # capabilities read from conductor-agent.env
./install.sh --caps "linux,web,backend,quality-audit,e2e"   # or pass explicitly
```

Idempotent — safe to re-run any time (e.g. after pulling an updated script). After a
format: reinstall the agent, then `git pull && ./install.sh` restores the quality layer.

## What it installs

| Layer | Files | Gate/when |
|---|---|---|
| **Deterministic gates** (per-commit) | `lib/{i18n-render-check,honesty-grep,fabrication-data-check,fabrication-audit}.sh` | wired HARD into every recipe `gate-verify.sh` |
| **i18n merge-driver** (kills merge-hotspot) | `lib/json-merge-driver.py` + git config | `messages/*.json` additive deep-merge (repos' `.gitattributes` reference it) |
| **Semantic auditor loop** | `lib/auditor*.{sh,py,txt}` + `auditor-cron.sh` | cap `quality-audit`/`web`; every 3h → fix-tasks |
| **E2E + visual runner** | `lib/e2e-*.{sh,py}` + `e2e-cron.sh` | cap `e2e`; every 6h → screenshots + fix-tasks |

## Capability model

The host declares `CONDUCTOR_CAPABILITIES` (already used by the agent for lane routing).
The installer enables features by capability — **add a capability to add a feature**:

| Capability | Enables |
|---|---|
| `web` / `backend` | agent core + semantic auditor cron |
| `quality-audit` | (alias) semantic auditor cron |
| `e2e` | real-backend Playwright + visual runner cron (needs a `<project>-e2e.env`) |
| `ios` | iOS build/verify steps (expects a **darwin + Xcode** host) — off on Linux |

So a Linux worker without `e2e`/`ios` simply doesn't get those crons/steps; a Mac with
`ios` does. Nothing is machine-baked.

## Host-local (NOT in git)

Secrets stay on the host: `~/conductor-agent/*-e2e.env` (staging creds), the gateway
token, git credentials. `env-templates/*.example` are seeded on install with the
non-secret shape — fill the secrets on the box. `.gitattributes` (the merge-driver
binding) is versioned **in the target repos**, not here.

## Scope today vs full portability

This kit **fully solves format-recovery for the current host (davinci)**: after a wipe,
`git pull && ./install.sh` restores the entire quality layer to the same paths.

Two follow-ups make it portable to an **arbitrary** host/user (not required for recovery):

1. **Hardcoded `/home/davinci` paths** in ~6 lib scripts (e.g. `CLAUDE_CONFIG_DIR`,
   artifact roots) should read `$HOME`/`$CONDUCTOR_AGENT_HOME`. `install.sh` itself is
   already `$AGENT_HOME`-relative — only the lib bodies need the sweep.
2. **Project list** (`xirigo-admin`, `xirigo-vendor`) is inline in `auditor-cron.sh` /
   `e2e-cron.sh`; moving it to a `projects.conf` makes the crons project-agnostic.

Both are mechanical passes; the capability-gated framework (add a capability → add a
feature: `e2e`, `ios`, …) is already in place and proven.
