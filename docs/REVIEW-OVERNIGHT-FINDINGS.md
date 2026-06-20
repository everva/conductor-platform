# Overnight adversarial review — findings (2026-06-20)

A fresh, full-platform adversarial pass run during the autonomous overnight session,
across four lenses: **security/secrets**, **Go correctness / frozen contracts**,
**test integrity / honesty**, and **build/release (4D-3)**. It concentrated on this
session's new, previously-unreviewed code (the onboard-500 fix, the G3/G4 editor
live tests, and the 4D-3 signing pipeline) and spot-checked the crown-jewel
invariants on the older Faz-1→3 code (which already had two prior adversarial
passes, 0 crit/high — see REVIEW-FAZ4-FINDINGS.md, REVIEW-FAZ4-CLOSING-FINDINGS.md).

> Method note: the planned 4-way *parallel agent* fan-out could not run — subagents
> spawn as `claude` subprocesses and this nested-agent sandbox has no `claude`
> subscription auth ("Not logged in"), the same constraint that blocks the real-claude
> distill (G3) and the realclaude e2e here. The review was therefore conducted
> directly. (This auth limitation is environmental, not a product defect.)

## Findings fixed (2 MEDIUM)

### M1 — Concurrent migration race (daemon + gateway cold start) · FIXED `a86007e`
The onboard-500 fix made the API gateway run `Migrate` on startup, turning it into a
SECOND migrator alongside the daemon. goose's `Up` takes no lock unless
`WithSessionLocker` is set, so a simultaneous cold start against a FRESH database
raced two goose runs into a duplicate-DDL conflict.
- **Proven:** without a lock, a 6-way concurrent migrate fails ~half the time with
  `duplicate key value violates unique constraint "pg_type_typname_nsp_index" (SQLSTATE 23505)`.
- **Fix:** `runGooseUp` now passes `goose.WithSessionLocker(lock.NewPostgresSessionLocker())`
  (a Postgres advisory lock on a dedicated connection for the duration of `Up`).
  Concurrent migrators serialize; the loser finds the schema current (a no-op).
  Benefits BOTH binaries (shared path); additive. `migrate_concurrent_test.go`
  races 6 migrators and is teeth-verified (fails without the locker).

### M2 — git option-injection via onboard repo/base_branch (auth→RCE) · FIXED `a1b30af`
The gateway validated `repo` against leading-dash injection, but the CLI onboard did
not validate at all, the gateway did not validate `base_branch` (flows to `git fetch`/
`worktree`), and the provisioner's `git clone` lacked a `--` guard. `git clone/fetch
--upload-pack=<cmd>` is remote code execution.
- **Fix:** new `internal/gitsafe.ValidArg` (reject empty / leading-"-" / whitespace /
  control chars) applied at BOTH onboard entries for repo + base_branch; the
  provisioner runs `git clone … -- <repo> <dir>` so repo is always positional.
- **Tests:** gitsafe unit table; gateway `base_branch` rejection; CLI repo+base
  rejection; existing valid-clone provisioner tests still green.

## Lenses with no new findings

- **Security/secrets:** CLEAN. No key/secret material git-tracked in either repo
  (`git ls-files` scan). No token/DSN in any log call (only the backend *name*,
  `storeBackendName(cfg.dsn)` → "postgres"/"memory"). The new `serverError(op,err)`
  logs a static op label + a secret-free query error. sign/notarize/ci-keychain do
  not echo key material; release.yml confines secrets to one shell step.
- **Frozen contracts (ADR-0021):** intact. This session changed no interface
  definition (`statestore.StateStore`, `engine` adapter, `events` bus/envelope) —
  `git diff` of those files across the session is empty. Changes were additive.
- **Merge authority:** the deterministic gate remains the only merge path
  (Develop → Verify → independent-pass-only squash-merge); the prior e2e proves the
  negative case (a fake "pass" + non-compiling code) blocks the merge.
- **Test integrity / honesty:** the new tests have teeth (onboard_pg + concurrent
  migrate both teeth-verified; gitsafe + onboard-validation tables are explicit) and
  the env-gated editor live tests (G1/G3/G4) skip-as-skip. Docs were corrected to
  match reality (editor/README: G1/G3/G4 are committed tests, G3 live needs an authed
  `claude -p`, only `/events` backfill remains a manual check).
- **Build/release (4D-3):** `sign.sh` is validated by a REAL Apple notarization
  acceptance (`spctl` → Notarized Developer ID); `notarize.sh` EXIT-trap returns 0 on
  success; `ci-keychain.sh` shreds the temp .p12 and emits only the keychain path
  (verified locally); `release.yml` is actionlint-clean and Infisical-sourced.

## Not covered / deferred
- A full line-by-line re-review of all Faz-1→3 code was not repeated (two prior
  adversarial passes; diminishing returns). The crown-jewel invariants were
  spot-checked and hold.
- Lower-priority hardening left as-is: the provisioner's other git verbs
  (`fetch`/`worktree`/`merge`) take base-branch values that are now validated at both
  onboard entries; an exec-site `--` on those too would be belt-and-suspenders.
