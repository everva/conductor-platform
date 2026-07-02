package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/everva/conductor-platform/internal/agentclient"
	"github.com/everva/conductor-platform/internal/conductor"
	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/provisioner"
	"github.com/everva/conductor-platform/internal/statestore"
	"github.com/everva/conductor-platform/internal/verify"
)

// ExecutorConfig configures the real Executor (the develop→verify→merge half). These
// come from the host-agent's flags/env on davinci.
type ExecutorConfig struct {
	// RootDir is the agent's writable workspace root (clones + worktrees).
	RootDir string
	// Repo is the git remote URL of the project (e.g. https://github.com/everva/optiway.git).
	Repo string
	// BaseBranch is the branch conductor work merges onto (e.g. conductor/optiway).
	// optiway's main is NEVER touched.
	BaseBranch string
	// GHToken authenticates git clone + push of the (private) repo. It is installed as
	// a credential helper by the provisioner and stripped from the performer subprocess
	// env by envsafe — the performer never sees it.
	GHToken string
	// DevelopCmd is the performer argv (default ["claude","-p"]).
	DevelopCmd []string
	// Timeout bounds a single develop subprocess.
	Timeout time.Duration
	// Gates are the deterministic verify gates (build/test/lint) — the sole merge
	// authority (Rule#9). Without gates the verifier has nothing to check.
	Gates []verify.Gate
	// Push enables pushing the merged base to the remote (so the director sees the
	// conductor/optiway branch update). Default true for a real host.
	Push bool
	// PushRemote is the git remote to push to (default "origin").
	PushRemote string
	// HoldoutStore serves the ADR-0018 hidden holdout the verify gate injects (Faz-S S3). nil →
	// noopHoldout (no holdout injected; public gates still run) so existing wiring is unchanged;
	// cmd/conductor-agent wires NewGatewayHoldout over the gateway's GET /agent/holdout.
	HoldoutStore verify.HoldoutStore
	// HoldoutCmd is the argv that RUNS the injected holdout in the verify-worktree (Faz-S S3b),
	// e.g. ["pnpm","exec","playwright","test"]. Required only when a scenario actually has a stored
	// holdout body (empty/absent holdouts are skipped); empty here → a present holdout errors
	// ("no holdout command configured"), which is the honest signal to configure it.
	HoldoutCmd []string
	// ReviewEnabled turns on the STRICT third-eye LLM review that GATES the auto-merge: after the
	// deterministic gate passes, a SEPARATE fresh-eyes claude adversarially reviews the diff vs the
	// acceptance; changes-requested → re-develop/re-verify/re-review (cap MaxReviewRounds); only a
	// CLEAN review yields pass. Default true (cmd/conductor-agent -review). When false the executor
	// behaves EXACTLY as before (the deterministic gate alone decides — no behavior change).
	ReviewEnabled bool
}

// gatewayHoldout adapts a gateway holdout-fetch func to verify.HoldoutStore so the agent's verifier
// injects the hidden holdout the gateway serves (Faz-S S3). An empty ref or a not-found holdout
// yields an empty holdout (the deterministic public gates still run); the body is never logged.
type gatewayHoldout struct {
	fetch func(ctx context.Context, ref string) (name string, files map[string][]byte, found bool, err error)
}

func (g gatewayHoldout) Fetch(ctx context.Context, ref string) (verify.Holdout, error) {
	if strings.TrimSpace(ref) == "" {
		return verify.Holdout{}, nil
	}
	name, files, found, err := g.fetch(ctx, ref)
	if err != nil {
		return verify.Holdout{}, err
	}
	if !found {
		return verify.Holdout{}, nil
	}
	return verify.Holdout{Name: name, Files: files}, nil
}

// NewGatewayHoldout builds a verify.HoldoutStore backed by a gateway fetch func (cmd/conductor-agent
// passes client.GetHoldout). Wired into ExecutorConfig.HoldoutStore.
func NewGatewayHoldout(fetch func(ctx context.Context, ref string) (string, map[string][]byte, bool, error)) verify.HoldoutStore {
	return gatewayHoldout{fetch: fetch}
}

// noopHoldout is a HoldoutStore that injects nothing — used until the gateway serves
// holdouts (Faz G3). The verifier still runs the real gates; only the ADR-0018 hidden
// holdout is absent.
type noopHoldout struct{}

func (noopHoldout) Fetch(_ context.Context, _ string) (verify.Holdout, error) {
	return verify.Holdout{}, nil
}

// developer is the slice of *engine.CommandEngine the review loop needs (re-develop against the
// reviewer's findings). A fake satisfies it in tests; *engine.CommandEngine satisfies it in prod.
type developer interface {
	Develop(ctx context.Context, task statestore.Task, ws engine.Workspace) (engine.Verdict, error)
}

// gateVerifier is the slice of *verify.Verifier the review loop needs (re-run the DETERMINISTIC
// gate after a re-develop — the gate still rules, Rule#9). A fake satisfies it in tests.
type gateVerifier interface {
	Verify(ctx context.Context, verdict engine.Verdict, ws engine.Workspace, gates []verify.Gate, holdoutRef string) (engine.ReviewResult, []engine.Check, error)
}

// reviewFunc is the injectable third-eye reviewer (production: runReviewClaude). The seam lets the
// reviewLoop test script changes-requested→pass without spawning `claude -p`, mirroring the
// engine's runnerFunc seam.
type reviewFunc func(ctx context.Context, wsPath string, acceptance []string, fullPatch string, onProgress func(string)) (engine.ReviewResult, error)

// patchFunc is the injectable full-file diff used as the reviewer's input (production: the
// GitDiffer). The seam lets the reviewLoop test drive the per-round diff (incl. the no-op guard)
// without a real git repo.
type patchFunc func(ctx context.Context, project statestore.Project, ws engine.Workspace) string

// RealExecutor wires the proven execution packages (provisioner + engine + verify +
// merger) behind the Executor seam. It runs one task at a time and holds that task's
// worktree between Run and Merge.
type RealExecutor struct {
	prov   *provisioner.Provisioner
	eng    developer
	verf   gateVerifier
	merger *conductor.GitMerger
	cfg    ExecutorConfig
	// review is the third-eye reviewer (default runReviewClaude); injectable for tests.
	review reviewFunc
	// patch computes the reviewer's full-file diff input (default the GitDiffer); injectable.
	patch patchFunc

	// mu guards ws + phase against the runner's concurrent ProgressProbe heartbeat.
	mu sync.Mutex
	// ws holds the live worktree per in-flight task (single task at a time; map for safety).
	ws map[string]engine.Workspace
	// phase is the current phase per in-flight task (provisioning|developing|verifying|reviewing),
	// read by Progress for the "Now" pulse.
	phase map[string]string
}

// NewRealExecutor builds the executor from config, mirroring the daemon's wiring
// (cmd/conductor) but local-to-this-host. A non-nil error means a misconfiguration
// (e.g. an unwritable RootDir).
func NewRealExecutor(cfg ExecutorConfig) (*RealExecutor, error) {
	if cfg.RootDir == "" {
		return nil, fmt.Errorf("agent executor: RootDir is required")
	}
	if len(cfg.DevelopCmd) == 0 {
		cfg.DevelopCmd = []string{"claude", "-p"}
	}
	if cfg.PushRemote == "" {
		cfg.PushRemote = "origin"
	}
	prov, err := provisioner.New(provisioner.Config{RootDir: cfg.RootDir, GHToken: cfg.GHToken})
	if err != nil {
		return nil, fmt.Errorf("agent executor: provisioner: %w", err)
	}
	eng := engine.NewCommandEngine(engine.RecipeConfig{DevelopCmd: cfg.DevelopCmd, Timeout: cfg.Timeout})
	// Faz-S S3: inject the gateway-backed holdout store when configured; else the no-op (no holdout
	// injected — the deterministic public gates still run). Keeps existing callers unchanged.
	holdouts := cfg.HoldoutStore
	if holdouts == nil {
		holdouts = noopHoldout{}
	}
	verf := verify.New(holdouts, verify.Config{HoldoutCmd: cfg.HoldoutCmd})
	merger := conductor.NewGitMerger(
		func(projectID string) string { return cfg.RootDir + "/clones/" + projectID },
		conductor.WithPush(conductor.PushConfig{Enabled: cfg.Push, Remote: cfg.PushRemote, GHToken: cfg.GHToken}),
	)
	re := &RealExecutor{prov: prov, eng: eng, verf: verf, merger: merger, cfg: cfg, review: runReviewClaude, ws: map[string]engine.Workspace{}, phase: map[string]string{}}
	// Default the reviewer's diff input to the real GitDiffer (best-effort: a diff error → "").
	// NORMAL diff (ReviewPatch), NOT the editor's whole-file FullPatch: the latter embeds each
	// changed file in full, so a few large generated files (i18n JSON) blow past the byte cap and
	// truncate later-sorting SOURCE files out of the reviewer's view (the reviewer then false-flags a
	// present change as "missing from the diff" and blocks a correct task).
	re.patch = func(ctx context.Context, project statestore.Project, ws engine.Workspace) string {
		patch, _, derr := conductor.NewGitDiffer().ReviewPatch(ctx, project, ws)
		if derr != nil {
			return ""
		}
		return patch
	}
	return re, nil
}

// setPhase records the current phase for a task (guarded; read by Progress).
func (e *RealExecutor) setPhase(taskID, phase string) {
	e.mu.Lock()
	e.phase[taskID] = phase
	e.mu.Unlock()
}

// Progress reports the task's current phase, how many files the performer has changed so far (git
// status count in the worktree), and a rich activity line (📖/✍️/🔎/🤔) read from the performer's
// claude transcript — what it is doing right now. Implements agent.ProgressProbe so the runner emits
// a live, non-repetitive pulse with a liveness signal during develop/verify. The transcript read is
// best-effort + read-only — it never touches the develop subprocess or its verdict. Safe to call
// concurrently with Run.
func (e *RealExecutor) Progress(taskID string) (string, int, string) {
	e.mu.Lock()
	phase := e.phase[taskID]
	ws, ok := e.ws[taskID]
	e.mu.Unlock()
	files := 0
	detail := ""
	if ok && ws.Path != "" {
		files = countChangedFiles(ws.Path)
		// Rich activity is develop-specific (read from the performer's claude transcript). During
		// provisioning/verify there is no live performer, so leave detail empty and let the generic
		// pulse carry the phase — never surface a STALE develop activity while the gate runs.
		if phase == "developing" {
			detail = latestDevelopActivity(ws.Path)
		}
	}
	return phase, files, detail
}

// Enhance materializes a fresh read-only checkout of the project at its base branch and runs
// claude there to turn a rough request into a detailed Turkish spec grounded in the real code
// (intake enhance, agent-side). It mutates nothing in the repo and never pushes — the worktree
// is a throwaway code view, removed when done. Satisfies the Enhancer seam.
func (e *RealExecutor) Enhance(ctx context.Context, projectID, roughSpec string, onProgress func(string)) (string, error) {
	project := statestore.Project{ID: projectID, Repo: e.cfg.Repo, BaseBranch: e.cfg.BaseBranch}
	dir, cleanup, err := e.prov.ReadOnlyCheckout(ctx, project)
	if err != nil {
		return "", fmt.Errorf("agent enhance: checkout: %w", err)
	}
	defer cleanup()
	return runEnhanceClaude(ctx, dir, roughSpec, onProgress)
}

// countChangedFiles returns the number of changed (staged/unstaged/untracked) files in a git
// worktree via `git status --porcelain`. Best-effort: any error → 0.
func countChangedFiles(dir string) int {
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// project rebuilds the statestore.Project the execution packages consume from the
// agent's config (the gateway holds the authoritative record; the agent only needs
// repo + base to clone/worktree/merge).
func (e *RealExecutor) project(projectID string) statestore.Project {
	return statestore.Project{ID: projectID, Repo: e.cfg.Repo, BaseBranch: e.cfg.BaseBranch}
}

// stateTask maps the gateway's TaskInfo into the statestore.Task the packages consume.
func stateTask(t agentclient.TaskInfo) statestore.Task {
	return statestore.Task{
		ID: t.ID, ProjectID: t.ProjectID, Lane: t.Lane, Tier: t.Tier,
		Requires: t.Requires, Deps: t.Deps, Branch: t.Branch, ScenarioID: t.ScenarioID,
	}
}

// Run provisions a worktree, develops (performer), and verifies (gate). The worktree
// is held for a later Merge.
//
// FLOW (the pipeline must not strand good work): two robustness rules keep a retried/blocked task
// moving to a real gate decision instead of looping:
//  1. RE-VERIFY-ON-RETRY — if a prior run already COMMITTED on this task's branch, re-verify that
//     commit instead of throwing it away and re-developing from scratch. A pass → held; gate findings
//     → the code needs changes, so fall through to a fresh develop; a verify ERROR (infra, e.g. a
//     broken test DB) is surfaced so the operator fixes it and retries → re-verify (not re-develop).
//  2. VERDICT-ROBUSTNESS — a develop that COMMITTED but emitted a malformed/absent final verdict JSON
//     still proceeds to verify: the performer's self-verdict is ignored anyway (Rule#9 — the gate is
//     the sole authority), so good committed code must reach the gate, never block on a parse nit.
func (e *RealExecutor) Run(ctx context.Context, taskInfo agentclient.TaskInfo, scenario agentclient.ScenarioInfo) (RunOutcome, error) {
	project := e.project(taskInfo.ProjectID)
	task := stateTask(taskInfo)
	branch := provisioner.BranchName(project.ID, task.ID)

	// (1) Re-verify a preserved branch from a prior run before re-developing.
	if ws, err := e.prov.WorkspaceForBranch(ctx, project, task, branch); err == nil {
		if aheadOfBase(ws.Path, project.BaseBranch) {
			e.mu.Lock()
			e.ws[task.ID] = ws
			e.mu.Unlock()
			e.setPhase(task.ID, "verifying")
			review, checks, verr := e.verf.Verify(ctx, engine.Verdict{}, ws, e.cfg.Gates, scenario.HoldoutRef)
			if verr != nil {
				return RunOutcome{}, fmt.Errorf("re-verify preserved branch: %w", verr)
			}
			if review.Result == "pass" {
				// The DETERMINISTIC gate passed; the STRICT third-eye review now GATES the merge
				// (changes-requested → re-develop/re-verify/re-review → only a clean review passes).
				if e.cfg.ReviewEnabled {
					review = e.reviewLoop(ctx, project, task, ws, scenario, review)
				}
				return e.buildOutcome(ctx, project, ws, review, checks), nil
			}
			// The preserved commit fails the gate → it genuinely needs changes; discard + develop fresh.
			_ = e.prov.Cleanup(ctx, ws)
			e.mu.Lock()
			delete(e.ws, task.ID)
			e.mu.Unlock()
		} else {
			_ = e.prov.Cleanup(ctx, ws) // branch exists but carries no work → develop fresh
		}
	}

	review, checks, ws, err := e.freshDevelopReview(ctx, project, taskInfo, scenario)
	if err != nil {
		return RunOutcome{}, err
	}
	return e.buildOutcome(ctx, project, ws, review, checks), nil
}

// freshDevelopReview provisions a FRESH worktree off the CURRENT base, develops the task (performer),
// runs the deterministic gate (with the gate-correction loop), then the strict third-eye review. It
// is the shared from-scratch core: Run uses it for the initial development, and Merge uses it to
// RE-DEVELOP when an approved task's base drifted into a merge conflict (re-implementing on the
// current base, conflict-free). Returns the final review + per-gate checks + the live worktree.
func (e *RealExecutor) freshDevelopReview(ctx context.Context, project statestore.Project, taskInfo agentclient.TaskInfo, scenario agentclient.ScenarioInfo) (engine.ReviewResult, []engine.Check, engine.Workspace, error) {
	task := stateTask(taskInfo)

	e.setPhase(task.ID, "provisioning")
	ws, err := e.prov.Workspace(ctx, project, task)
	if err != nil {
		return engine.ReviewResult{}, nil, engine.Workspace{}, fmt.Errorf("provision workspace: %w", err)
	}
	e.mu.Lock()
	e.ws[task.ID] = ws
	e.mu.Unlock()

	// The scenario (acceptance) lives in the gateway, not the checkout — so the agent writes it
	// into the worktree as .conductor/TASK.md for the performer to read (the develop prompt points
	// claude at it). Best-effort: a write failure shouldn't sink the run (claude still has the
	// stdin contract); log via the returned error only if it's a hard FS error.
	if err := writeTaskBrief(ws.Path, taskInfo, scenario); err != nil {
		return engine.ReviewResult{}, nil, engine.Workspace{}, fmt.Errorf("write task brief: %w", err)
	}

	e.setPhase(task.ID, "developing")
	verdict, derr := e.eng.Develop(ctx, task, ws)
	if derr != nil {
		// (2) Verdict-robustness: a develop that COMMITTED work proceeds to verify even if the final
		// verdict JSON was malformed/absent — the gate is the authority (Rule#9). A develop with NO
		// commit is a real block (auth wall, empty output, killed before any work) → surface it.
		if (errors.Is(derr, engine.ErrMalformedVerdict) || errors.Is(derr, engine.ErrNoVerdict)) && aheadOfBase(ws.Path, project.BaseBranch) {
			verdict = engine.Verdict{}
		} else {
			return engine.ReviewResult{}, nil, engine.Workspace{}, fmt.Errorf("develop: %w", derr)
		}
	}

	e.setPhase(task.ID, "verifying")
	// Faz-S S3: verify against the scenario's hidden holdout (ADR-0018). The HoldoutStore (gateway-
	// backed in prod) fetches it; an empty ref / absent holdout → public gates only (unchanged).
	review, checks, err := e.verf.Verify(ctx, verdict, ws, e.cfg.Gates, scenario.HoldoutRef)
	if err != nil {
		return engine.ReviewResult{}, nil, engine.Workspace{}, fmt.Errorf("verify: %w", err)
	}
	if review.Result == "changes-requested" && e.cfg.ReviewEnabled {
		// The DETERMINISTIC gate REJECTED the change (build/lint/parity). Instead of immediately
		// blocking, the AGENT re-develops to FIX the gate failure itself (cap MaxGateRounds). A
		// now-passing gate flows into the third-eye review below; a still-failing gate stays
		// changes-requested (a held task for the director).
		review = e.gateCorrectLoop(ctx, project, task, ws, scenario, review)
	}
	if review.Result == "pass" && e.cfg.ReviewEnabled {
		// The DETERMINISTIC gate passed (sole MERGE authority, Rule#9); the STRICT third-eye review
		// now GATES the auto-merge. Only a CLEAN review keeps Result=="pass"; otherwise the loop
		// re-develops against the findings, re-verifies (the gate STILL rules), and re-reviews, and
		// if still unresolved returns changes-requested (a held task for the director).
		review = e.reviewLoop(ctx, project, task, ws, scenario, review)
	}
	return review, checks, ws, nil
}

// reviewLoop runs the STRICT third-eye review AFTER the deterministic gate has already passed, and
// GATES the auto-merge on it. It is only called when review.Result=="pass" and ReviewEnabled.
//
// CONTRACT (the only path that keeps a pass is a CLEAN review):
//   - Compute the full-file diff (best-effort; on error the reviewer judges an empty patch).
//   - First review: pass → return the ORIGINAL gate pass (merge proceeds).
//   - Otherwise loop up to MaxReviewRounds: write the findings to .conductor/REVIEW.md, re-develop
//     against them, re-run the DETERMINISTIC gate (it STILL rules — a re-develop that breaks the
//     gate returns that gate's changes-requested), guard against a NO-OP re-develop (identical diff
//     → unresolved), then re-review. A clean re-review → return the gate pass.
//   - Exhausted without a clean review → changes-requested ("needs user"): a held task, never a
//     fabricated pass.
//
// FAIL-CLOSED throughout: runReviewClaude already maps any reviewer infra failure to
// changes-requested, so an unreachable reviewer holds for the director rather than auto-passing.
func (e *RealExecutor) reviewLoop(ctx context.Context, project statestore.Project, task statestore.Task, ws engine.Workspace, scenario agentclient.ScenarioInfo, review engine.ReviewResult) engine.ReviewResult {
	e.setPhase(task.ID, "reviewing")

	// fullPatch is the whole-file branch-vs-base diff the reviewer judges. Best-effort: a diff
	// failure → "" (the reviewer is told an empty patch is itself suspicious).
	fullPatch := e.patch(ctx, project, ws)

	rr, _ := e.review(ctx, ws.Path, scenario.Acceptance, fullPatch, nil)
	if rr.Result == "pass" {
		return review // clean on the first read → keep the deterministic gate's pass
	}

	for round := 0; round < MaxReviewRounds; round++ {
		// Hand the findings to the developer as .conductor/REVIEW.md feedback, then re-develop.
		if err := writeReviewFeedback(ws.Path, rr); err != nil {
			return engine.ReviewResult{Result: "changes-requested", Summary: "write review feedback failed: " + err.Error(), Findings: rr.Findings}
		}
		e.setPhase(task.ID, "developing")
		// Re-develop against the findings. Verdict-robustness mirrors Run: a re-develop that COMMITTED
		// but emitted a malformed/absent verdict still proceeds (the gate below is the authority,
		// Rule#9); a re-develop that produced NO commit is a real failure → changes-requested.
		if _, err := e.eng.Develop(ctx, task, ws); err != nil {
			committed := (errors.Is(err, engine.ErrMalformedVerdict) || errors.Is(err, engine.ErrNoVerdict)) && aheadOfBase(ws.Path, project.BaseBranch)
			if !committed {
				return engine.ReviewResult{Result: "changes-requested", Summary: "re-develop after review failed: " + err.Error()}
			}
		}

		// The DETERMINISTIC gate STILL rules (Rule#9): a re-develop that breaks the gate is rejected
		// with the gate's own changes-requested, regardless of what the reviewer would say.
		e.setPhase(task.ID, "verifying")
		gate, _, gerr := e.verf.Verify(ctx, engine.Verdict{}, ws, e.cfg.Gates, scenario.HoldoutRef)
		if gerr != nil {
			return engine.ReviewResult{Result: "changes-requested", Summary: "re-verify after review failed: " + gerr.Error()}
		}
		if gate.Result != "pass" {
			return gate // the deterministic gate now fails → it decides
		}

		// No-op guard: if the re-develop produced NO change, the reviewer's concern is unresolved and
		// re-reviewing the identical diff would loop — hold for the director instead.
		newPatch := e.patch(ctx, project, ws)
		if newPatch == fullPatch {
			return engine.ReviewResult{Result: "changes-requested", Summary: "reviewer unresolved — developer made no change", Findings: rr.Findings}
		}
		fullPatch = newPatch

		e.setPhase(task.ID, "reviewing")
		rr, _ = e.review(ctx, ws.Path, scenario.Acceptance, fullPatch, nil)
		if rr.Result == "pass" {
			return review // a clean re-review → the change may merge
		}
	}

	// Exhausted the cap without a clean review: hold for the director (never an auto-pass).
	return engine.ReviewResult{
		Result:   "changes-requested",
		Summary:  fmt.Sprintf("third-eye review unresolved after %d rounds — needs user", MaxReviewRounds),
		Findings: rr.Findings,
	}
}

// gateCorrectLoop runs the DETERMINISTIC-gate self-correction BEFORE the third-eye review: when the
// FIRST gate (build/lint/parity) returns changes-requested, the AGENT re-develops to FIX the gate
// failure itself instead of immediately blocking. It mirrors reviewLoop exactly — only the feedback
// (the gate's findings, written to .conductor/GATE.md) and the loop authority (the gate, not a
// reviewer) differ. It is only called when the first verify returned changes-requested and
// ReviewEnabled.
//
// CONTRACT (the only path that yields a pass is a now-clean gate):
//   - Loop up to MaxGateRounds: write the gate's findings to .conductor/GATE.md, re-develop against
//     them, re-run the DETERMINISTIC gate. A gate pass → return it (Run then flows into reviewLoop).
//   - Re-develop verdict-robustness mirrors Run: a re-develop that COMMITTED but emitted a
//     malformed/absent verdict still proceeds (the gate below is the authority, Rule#9); a
//     re-develop that produced NO commit is a real failure → changes-requested.
//   - No-progress guard: compute the FullPatch each round; an IDENTICAL patch means the re-develop
//     changed nothing → break early (changes-requested, "made no change"), never loop on the same
//     diff.
//   - Exhausted without a clean gate → changes-requested ("needs user"): a held task for the
//     director, never a fabricated pass.
//
// The DETERMINISTIC gate remains the sole MERGE authority throughout (Rule#9): this loop never
// fabricates a pass; it only gives the developer bounded chances to fix what the gate flagged.
func (e *RealExecutor) gateCorrectLoop(ctx context.Context, project statestore.Project, task statestore.Task, ws engine.Workspace, scenario agentclient.ScenarioInfo, review engine.ReviewResult) engine.ReviewResult {
	// lastPatch tracks the prior round's full-file diff for the no-progress guard. Seed it with the
	// rejected attempt's diff so a re-develop that changes NOTHING is caught on the first round.
	lastPatch := e.patch(ctx, project, ws)
	lastFindings := review.Findings

	for round := 0; round < MaxGateRounds; round++ {
		// Hand the gate's findings to the developer as .conductor/GATE.md feedback, then re-develop.
		if err := writeGateFeedback(ws.Path, review); err != nil {
			return engine.ReviewResult{Result: "changes-requested", Summary: "write gate feedback failed: " + err.Error(), Findings: review.Findings}
		}
		e.setPhase(task.ID, "developing")
		// Re-develop to fix the gate failure. Verdict-robustness mirrors Run: a re-develop that
		// COMMITTED but emitted a malformed/absent verdict still proceeds (the gate below is the
		// authority, Rule#9); a re-develop that produced NO commit is a real failure → changes-requested.
		if _, derr := e.eng.Develop(ctx, task, ws); derr != nil {
			committed := (errors.Is(derr, engine.ErrMalformedVerdict) || errors.Is(derr, engine.ErrNoVerdict)) && aheadOfBase(ws.Path, project.BaseBranch)
			if !committed {
				return engine.ReviewResult{Result: "changes-requested", Summary: "re-develop after gate failure failed: " + derr.Error()}
			}
		}

		// Re-run the DETERMINISTIC gate (it STILL rules, Rule#9). A pass → return it so Run flows into
		// the reviewLoop; a verify ERROR is surfaced as changes-requested with the cause.
		e.setPhase(task.ID, "verifying")
		gate, _, gerr := e.verf.Verify(ctx, engine.Verdict{}, ws, e.cfg.Gates, scenario.HoldoutRef)
		if gerr != nil {
			return engine.ReviewResult{Result: "changes-requested", Summary: "re-verify after gate failure failed: " + gerr.Error()}
		}
		if gate.Result == "pass" {
			return gate // the gate now passes → Run proceeds to the third-eye review
		}
		review = gate
		lastFindings = gate.Findings

		// No-progress guard: if the re-develop produced NO change, the gate failure is unresolved and
		// re-verifying the identical diff would loop — hold for the director instead.
		newPatch := e.patch(ctx, project, ws)
		if newPatch == lastPatch {
			return engine.ReviewResult{Result: "changes-requested", Summary: "gate unresolved — developer made no change", Findings: lastFindings}
		}
		lastPatch = newPatch
	}

	// Exhausted the cap without a clean gate: hold for the director (never an auto-pass).
	return engine.ReviewResult{
		Result:   "changes-requested",
		Summary:  fmt.Sprintf("gate failure unresolved after %d self-correction rounds — needs user", MaxGateRounds),
		Findings: lastFindings,
	}
}

// buildOutcome assembles the RunOutcome (result + branch + checks) and best-effort attaches the
// branch-vs-base diff (review parity — observability only; a diff failure never fails the run, the
// verdict stands). The bounded summary rides a KindDiff event; the full-file patch is stored for the
// native side-by-side diff. Shared by the develop and the re-verify paths so both surface the change.
func (e *RealExecutor) buildOutcome(ctx context.Context, project statestore.Project, ws engine.Workspace, review engine.ReviewResult, checks []engine.Check) RunOutcome {
	out := RunOutcome{
		Result:   review.Result,
		Branch:   ws.Branch,
		Summary:  review.Summary,
		Checks:   toChecks(checks),
		Findings: review.Findings,
	}
	differ := conductor.NewGitDiffer()
	if summary, derr := differ.Diff(ctx, project, ws); derr == nil {
		out.Diff = &summary
		if full, truncated, ferr := differ.FullPatch(ctx, project, ws); ferr == nil {
			out.FullPatch = full
			out.FullTruncated = truncated
		}
	}
	return out
}

// aheadOfBase reports whether the worktree HEAD has at least one commit beyond the base branch (the
// performer committed work). Best-effort: any git error → false (treat as no work). Used by the two
// FLOW rules above to tell "good committed code worth verifying" apart from "an empty/base cut".
func aheadOfBase(wsPath, base string) bool {
	out, err := exec.Command("git", "-C", wsPath, "rev-list", "--count", base+"..HEAD").Output()
	if err != nil {
		return false
	}
	n := strings.TrimSpace(string(out))
	return n != "" && n != "0"
}

// Merge squash-merges the verified branch. For an APPROVED held task it re-attaches
// the branch if needed, merges the current base in (drift guard), and re-verifies
// before merging — mirroring the daemon's mergeApproved (never merge stale work).
func (e *RealExecutor) Merge(ctx context.Context, taskInfo agentclient.TaskInfo, scenario agentclient.ScenarioInfo, branch string, approved bool) (MergeResult, error) {
	project := e.project(taskInfo.ProjectID)
	task := stateTask(taskInfo)

	e.mu.Lock()
	ws, ok := e.ws[task.ID]
	e.mu.Unlock()
	if !ok {
		// Worktree gone (e.g. a fresh agent run after a restart): re-attach the branch.
		w, err := e.prov.WorkspaceForBranch(ctx, project, task, branch)
		if err != nil {
			return MergeResult{}, fmt.Errorf("re-attach branch %q: %w", branch, err)
		}
		ws = w
		e.mu.Lock()
		e.ws[task.ID] = ws
		e.mu.Unlock()
	}

	if approved {
		// Drift guard: merge the current base into the held branch, then re-run the cheap gate.
		if err := e.prov.MergeBaseIntoWorktree(ctx, project, ws); err != nil {
			if e.prov.IsBaseMergeConflict(err) {
				// The base drifted into a conflict while the task was held (a concurrent merge touched the
				// same lines). Rather than refuse and STICK the approved task forever, RE-DEVELOP it fresh
				// against the CURRENT base — the performer re-implements on the new develop, conflict-free
				// — then re-run the deterministic gate + strict third-eye review. The gate+review STILL
				// gate the merge (Rule#9): a bad re-develop holds (changes-requested), only a clean one
				// proceeds, so no unreviewed code can merge. The freshly-built branch sits ON the current
				// base, so the squash-merge below is conflict-free.
				_ = e.prov.Cleanup(ctx, ws)
				e.mu.Lock()
				delete(e.ws, task.ID)
				e.mu.Unlock()
				rr, _, freshWs, ferr := e.freshDevelopReview(ctx, project, taskInfo, scenario)
				if ferr != nil {
					return MergeResult{}, fmt.Errorf("re-develop after base drift: %w", ferr)
				}
				if rr.Result != "pass" {
					return MergeResult{}, fmt.Errorf("approved merge refused: base drifted, re-develop did not pass gate/review")
				}
				ws = freshWs
			} else {
				return MergeResult{}, fmt.Errorf("merge base into held branch: %w", err)
			}
		} else {
			// Base merged cleanly → re-run the cheap gate to confirm the drift didn't break it.
			review, _, verr := e.verf.Verify(ctx, engine.Verdict{}, ws, e.cfg.Gates, "")
			if verr != nil {
				return MergeResult{}, fmt.Errorf("re-verify after approval: %w", verr)
			}
			if review.Result != "pass" {
				return MergeResult{}, fmt.Errorf("approved merge refused: re-verify failed (base drift broke the gate)")
			}
		}
	}

	// P2b: capture the full-file diff from the verified worktree (branch tip, base merged in for
	// approved tasks) BEFORE the squash-merge collapses it, so the runner persists a TaskDiff on
	// EVERY merge path — closing the once-per-Run early-emit gaps. Best-effort: a diff failure never
	// fails the merge (observability only; mirrors buildOutcome).
	res := MergeResult{Branch: ws.Branch}
	differ := conductor.NewGitDiffer()
	if summary, derr := differ.Diff(ctx, project, ws); derr == nil {
		res.Base = summary.Base
		if full, truncated, ferr := differ.FullPatch(ctx, project, ws); ferr == nil {
			res.Patch = full
			res.Truncated = truncated
		}
	}

	sha, err := e.merger.SquashMerge(ctx, project, task, ws)
	if err != nil {
		return MergeResult{}, fmt.Errorf("squash-merge: %w", err)
	}
	res.SHA = sha
	return res, nil
}

// Cleanup removes the task's worktree (best-effort).
func (e *RealExecutor) Cleanup(ctx context.Context, taskInfo agentclient.TaskInfo) {
	e.mu.Lock()
	ws, ok := e.ws[taskInfo.ID]
	e.mu.Unlock()
	if !ok {
		return
	}
	_ = e.prov.Cleanup(ctx, ws)
	e.mu.Lock()
	delete(e.ws, taskInfo.ID)
	delete(e.phase, taskInfo.ID)
	e.mu.Unlock()
}

// writeTaskBrief writes the scenario (title + acceptance) into the worktree as
// .conductor/TASK.md so the performer can read WHAT to build (the scenario lives in the gateway,
// not the checkout — gateway-mediated model). The develop prompt points claude at this file. The
// brief is the director-authored task spec; the prompt instructs claude not to commit it.
func writeTaskBrief(wsPath string, task agentclient.TaskInfo, scenario agentclient.ScenarioInfo) error {
	dir := filepath.Join(wsPath, ".conductor")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	title := scenario.Title
	if title == "" {
		title = task.ID
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "Task: %s · Scenario: %s · Lane: %s · Tier: %s\n\n", task.ID, scenario.ID, task.Lane, task.Tier)
	b.WriteString("## Acceptance criteria\n")
	if len(scenario.Acceptance) == 0 {
		b.WriteString("- (none provided — use your judgement for a minimal, correct change)\n")
	}
	for _, a := range scenario.Acceptance {
		fmt.Fprintf(&b, "- %s\n", a)
	}
	// The STRICT third-eye reviewer (review.go) writes its findings to .conductor/REVIEW.md when it
	// requests changes; on a re-develop that file is present. Point the performer at it explicitly so
	// the correction loop actually addresses the review (the change cannot merge until a clean review).
	b.WriteString("\n## Reviewer feedback — READ FIRST if present\n")
	b.WriteString("If `.conductor/REVIEW.md` exists, a STRICT third-eye reviewer REQUESTED CHANGES on your previous attempt. ")
	b.WriteString("Read it FIRST and fix EVERY finding before anything else — the change CANNOT merge until a clean review passes.\n")
	// The DETERMINISTIC gate (build/lint/parity) writes its failure to .conductor/GATE.md when it
	// rejects an attempt; on a self-correction re-develop that file is present. Point the performer at
	// it explicitly so the re-develop actually fixes the build/lint errors the gate flagged.
	b.WriteString("If `.conductor/GATE.md` exists, the deterministic gate FAILED on a previous attempt — read it and fix EVERY issue first.\n")
	return os.WriteFile(filepath.Join(dir, "TASK.md"), []byte(b.String()), 0o644)
}

// toChecks maps engine.Check (the gate evidence) onto the wire Check shape.
func toChecks(checks []engine.Check) []agentclient.Check {
	out := make([]agentclient.Check, 0, len(checks))
	for _, c := range checks {
		out = append(out, agentclient.Check{Name: c.Name, Result: c.Result, Evidence: c.Evidence})
	}
	return out
}
