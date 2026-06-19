# Faz-4 Closing Adversarial Review — Findings (2026-06-19)

Closing review of the Faz-4 surface NOT covered by `REVIEW-FAZ4-FINDINGS.md` (that covered 4C): **4E** (live
integration + the realclaude/live opt-in tests) and **4D** (the Code-OSS fork overlay build, `everva/conductor-editor`).
3 parallel read-only lenses + orchestrator synthesis + fixes. **0 CRITICAL / 0 HIGH.**

## Lens 1 — Frozen contracts / additive-only (ADR-0021): ✅ CLEAN
git-verified: no Faz-4 commit (4A→4E) touched `internal/engine/engine.go`, `internal/statestore/`, or
`internal/events/event.go` (`git log <faz4-base>..HEAD --` over them is empty). The `Differ` seam is optional + nil-safe
(mirrors `Emitter`; `emitDiff` guards `c.differ == nil`); `events.DiffSummary` + `conductor.GitDiffer` are additive new
files reusing the pre-existing frozen `KindDiff`. `e2e_realclaude_test.go` is double-gated (`//go:build e2e && realclaude`
→ excluded from `go test ./...` AND CI's `-tags e2e`). `go build/vet ./...` clean. **0 findings.**

## Lens 2 — Token discipline + supply-chain: SOUND (2 MEDIUM fixed)
Token discipline sound: the bearer token rides ONLY SecretStorage + the host `Authorization` header / WS `?token=` query;
leak-guard tests real; CI `CP_REPO_TOKEN` used only as the `actions/checkout` token (never echoed); `jq -r` values are
quoted (no injection); `ws` is lockfile-pinned; no committed binaries (`vscode/` + the `.app` gitignored).
- **M1 (FIXED):** `@vscode/vsce` was range-pinned `^3.2.0` and fetched via `npx --yes` (newest 3.x at build time). →
  pinned **exact `3.2.0`** in `upstream.json` (npx now fetches exactly 3.2.0; npm-registry integrity per immutable
  version). *Documented follow-up (defense-in-depth):* add `@vscode/vsce@3.2.0` to `editor/` devDeps + invoke the local
  binary so the committed lockfile hash gates it.
- **M2 (FIXED):** the commit pin was read with `// empty`, so a missing `commit` field would silently skip verification.
  → `build/get-vscode.sh` now **requires** the commit pin (fails if absent/null) and always verifies `HEAD == commit`.
- L1/L3/L4: no action (query-param auth is the sanctioned channel + `wsConnector` never forwards the URL-bearing error;
  quoting + `git apply` flag-free are injection-safe). **L2 (FIXED):** added `expect(TOKEN).toBeTruthy()` in
  `diffObserver.live.test.ts` so the token-leak assertion is provably meaningful.

## Lens 3 — Genuineness / no-fake-green / no-overclaim: GENUINE (1 MEDIUM fixed)
Both opt-in tests are GENUINE: `e2e_realclaude_test.go` drives the real loop (real `claude -p` → real gate → real merge →
real `GitDiffer` KindDiff) with load-bearing assertions — a no-op/lying claude FAILS (traced: `ErrNoVerdict`→blocked, or
`greeting.go` absent + empty patch). `diffObserver.live.test.ts` resolves ONLY on a real live frame (no fallback timer →
times out→FAIL if none). Both `skip`-as-skip (not pass), gate-safe. No fake-green (the negative-path `TestE2E_GateBreak…`
asserts develop is unchanged).
- **MEDIUM (FIXED): overclaim.** G2/G3/G4 were framed in the same "✅ PASSED" block as G1, but only **G1** has a committed
  automated test; **G2** (`/events` backfill), **G3** (`/distill`), **G4** (`/approve`→merge) were verified **live by the
  orchestrator** (curl + daemon, runbook) — genuine manual Rule#9, but no committed test/captured artifact. → the ledger
  (`PHASE-4-PLAN.md` İLERLEME KAYDI) + `editor/README` "Live e2e check (4E)" now explicitly state the verification level
  (G1 = automated/committed; G2/G3/G4 = manual runbook).
- LOW: the 4D capstone is genuine (`verify-app.sh` is a real branding+built-in gate, `exit 1` on mismatch) but lives in
  the separate `everva/conductor-editor` repo and the `.app` is a local gitignored artifact (ledger discloses
  unsigned/manual — transparency-adequate; 4D-3 signed release is an explicit follow-up). The realclaude test proves
  plumbing (it dictates the file contents), not open-ended model capability — honest + commented.

## Verdict
Frozen contracts intact; token discipline + supply-chain sound (M1/M2 fixed); tests genuine with no fake-green; one
overclaim corrected (G2–G4 labeled manual-runbook). Faz-4 (4A→4E) + 4D-0/1/2 stand up to adversarial scrutiny.
