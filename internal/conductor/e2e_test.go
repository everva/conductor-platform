//go:build e2e

// Package conductor e2e: an END-TO-END validation that the product can actually
// drive a real project through onboard -> develop -> verify -> merge, and that a
// gate-breaking task is BLOCKED from merging (Rule#9, never fake-green).
//
// What is REAL here (no stubs on the load-bearing path):
//   - a throwaway LOCAL git repo (a tiny Go module on a `develop` branch) that
//     plays the part of a product-under-conductor; it is NOT conductor-platform;
//   - the real provisioner (genuine clone + per-task `git worktree` cut from
//     develop) — internal/provisioner;
//   - the real verifier (genuine `go build` / `go test` gates over the worktree)
//     — internal/verify;
//   - the real GitMerger (genuine `git merge --squash` into develop with the
//     `[task:<id>]` trailer) — internal/conductor;
//   - the real registry Picker + in-memory statestore — internal/{registry,statestore};
//   - the real conductor.Tick orchestration — internal/conductor;
//   - the REAL product engine (engine.CommandEngine) running a shell-script
//     "performer" in the worktree IN PLACE OF `claude -p`. The performer writes
//     real Go, commits it, and emits a schema-valid Verdict JSON on stdout, which
//     the product's own ParseVerdict/classifyOutput consumes (ADR-0014).
//
// Only the LLM brain is swapped for a deterministic script; the git/gate/merge/
// orchestration is the genuine article. Run with:
//
//	go test -tags e2e -run E2E -v ./internal/conductor/
package conductor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/governance"
	"github.com/everva/conductor-platform/internal/provisioner"
	"github.com/everva/conductor-platform/internal/registry"
	"github.com/everva/conductor-platform/internal/statestore"
	"github.com/everva/conductor-platform/internal/verify"
)

// e2eProjectID is the onboarded project's stable ID for the whole scenario.
const e2eProjectID = "e2e-proj"

// noopHoldout is a HoldoutStore that injects nothing: the e2e gate decision rides
// purely on the deterministic recipe gates (go build / go test) over the
// performer's real output. The holdout leg is exercised by verify's own tests; we
// keep it inert here so the merge decision is unambiguously the public gates.
type noopHoldout struct{}

func (noopHoldout) Fetch(_ context.Context, _ string) (verify.Holdout, error) {
	return verify.Holdout{Name: "e2e-noop", Files: nil}, nil
}

// TestE2E_OnboardDevelopVerifyMerge is the POSITIVE flow: a task whose performer
// writes compiling, test-passing Go is developed, independently verified, and
// squash-merged onto develop with a [task:<id>] trailer; the task ends done.
func TestE2E_OnboardDevelopVerifyMerge(t *testing.T) {
	requireGit(t)
	requireGo(t)
	ctx := context.Background()

	// --- the throwaway product-under-conductor: a real local git repo ---------
	upstream := newProductRepo(t)

	// --- onboard: project + a ready task + its scenario into the statestore ----
	store := statestore.NewMemoryStore()
	mustCreateProject(t, store, statestore.Project{
		ID:         e2eProjectID,
		Repo:       upstream, // a local path: the provisioner clones it for real.
		BaseBranch: "develop",
	})
	mustCreateScenario(t, store, statestore.Scenario{
		ID:         "scn-pos",
		ProjectID:  e2eProjectID,
		Title:      "add a Greeting helper with a passing test",
		HoldoutRef: "e2e-noop",
	})
	mustCreateTask(t, store, statestore.Task{
		ID:         "T-green",
		ProjectID:  e2eProjectID,
		Lane:       "build",
		Tier:       "T2",
		Status:     registry.StatusReady,
		ScenarioID: "scn-pos",
	})

	// --- the deterministic performer: writes GOOD Go, commits, emits Verdict ---
	performer := writePerformer(t, goodPerformerScript)

	root := t.TempDir()
	cond := buildConductor(t, store, root, performer)

	res, err := cond.Tick(ctx, e2eProjectID)
	if err != nil {
		t.Fatalf("positive tick returned error: %v (outcome=%s)", err, res.Outcome)
	}

	// --- assert the pipeline reached a real merge -----------------------------
	if res.Outcome != OutcomeMerged {
		t.Fatalf("positive flow: want outcome %q, got %q (review=%+v verdict=%+v)",
			OutcomeMerged, res.Outcome, res.Review, res.Verdict)
	}
	if res.MergeSHA == "" {
		t.Fatalf("positive flow: merged outcome but empty MergeSHA")
	}

	// statestore truth: the task is done.
	got := mustGetTask(t, store, "T-green")
	if got.Status != registry.StatusDone {
		t.Fatalf("positive flow: task status = %q, want %q", got.Status, registry.StatusDone)
	}

	// git truth: develop's tip carries the [task:<id>] trailer AND the new file.
	clone := filepath.Join(root, "clones", e2eProjectID)
	tip := gitT(t, clone, "log", "-1", "--format=%B", "develop")
	if !strings.Contains(tip, "[task:T-green]") {
		t.Fatalf("positive flow: develop tip missing [task:T-green] trailer; got:\n%s", tip)
	}
	files := gitT(t, clone, "ls-tree", "-r", "--name-only", "develop")
	if !strings.Contains(files, "greeting.go") {
		t.Fatalf("positive flow: greeting.go did not land on develop; tree:\n%s", files)
	}

	t.Logf("POSITIVE: outcome=%s mergeSHA=%s taskStatus=%s developTrailer=ok greeting.go landed",
		res.Outcome, res.MergeSHA[:min(12, len(res.MergeSHA))], got.Status)
}

// TestE2E_GateBreakBlocksMerge is the NEGATIVE flow (Rule#9): a task whose
// performer writes code that does NOT compile must fail the independent verify
// gate, never merge, and end blocked. We PROVE no fake-green: develop's tip is
// unchanged from the onboard baseline.
func TestE2E_GateBreakBlocksMerge(t *testing.T) {
	requireGit(t)
	requireGo(t)
	ctx := context.Background()

	upstream := newProductRepo(t)

	store := statestore.NewMemoryStore()
	mustCreateProject(t, store, statestore.Project{
		ID:         e2eProjectID,
		Repo:       upstream,
		BaseBranch: "develop",
	})
	mustCreateScenario(t, store, statestore.Scenario{
		ID:         "scn-neg",
		ProjectID:  e2eProjectID,
		Title:      "a task whose code breaks the build",
		HoldoutRef: "e2e-noop",
	})
	mustCreateTask(t, store, statestore.Task{
		ID:         "T-red",
		ProjectID:  e2eProjectID,
		Lane:       "build",
		Tier:       "T2",
		Status:     registry.StatusReady,
		ScenarioID: "scn-neg",
	})

	// This performer self-reports result=pass in its Verdict (a deliberately
	// lying performer) but writes Go that does NOT compile. The merge must ride
	// the INDEPENDENT gate, not the self-report (Rule#9).
	performer := writePerformer(t, badPerformerScript)

	root := t.TempDir()
	cond := buildConductor(t, store, root, performer)

	clone := filepath.Join(root, "clones", e2eProjectID)

	// A gate-breaking task is NOT blocked on the first tick: ADR-0004 gives it a
	// changes-requested RETRY budget (MaxRetries) before it is finally blocked. We
	// tick until the task reaches a terminal blocked state, asserting on EVERY tick
	// that the independent review never fake-greened and NOTHING merged. The final
	// state must be blocked — never done, never merged (Rule#9).
	var last TickResult
	var ticks int
	for ticks = 1; ticks <= MaxRetries+2; ticks++ {
		var err error
		last, err = cond.Tick(ctx, e2eProjectID)
		if err != nil {
			t.Fatalf("negative tick %d returned unexpected error: %v (outcome=%s)", ticks, err, last.Outcome)
		}

		// Never a merge, never a (fake) pass, on any tick.
		if last.Outcome == OutcomeMerged {
			t.Fatalf("negative flow: tick %d MERGED a gate-breaking task (Rule#9 violated): %+v", ticks, last)
		}
		if last.Review.Result == "pass" {
			t.Fatalf("negative flow: tick %d independent review fake-greened on broken code", ticks)
		}
		// Each pre-terminal tick is a retry (changes-requested under the cap).
		if last.Outcome == OutcomeBlocked {
			break
		}
		if last.Outcome != OutcomeRetry {
			t.Fatalf("negative flow: tick %d unexpected outcome %q (want retry or blocked)", ticks, last.Outcome)
		}
		// The retry must be driven by the INDEPENDENT gate, not the self-reported
		// verdict (which lies "pass").
		if last.Review.Result != "changes-requested" {
			t.Fatalf("negative flow: tick %d review = %q, want changes-requested", ticks, last.Review.Result)
		}
	}

	if last.Outcome != OutcomeBlocked {
		t.Fatalf("negative flow: task never reached blocked after %d ticks (last outcome %q)", ticks, last.Outcome)
	}

	// statestore truth: the task is terminally blocked, NOT done.
	got := mustGetTask(t, store, "T-red")
	if got.Status != registry.StatusBlocked {
		t.Fatalf("negative flow: final task status = %q, want %q", got.Status, registry.StatusBlocked)
	}

	// git truth (the proof of no fake-green): develop still has exactly one commit
	// (the onboard baseline) and NO [task:T-red] trailer anywhere.
	count := gitT(t, clone, "rev-list", "--count", "develop")
	if count != "1" {
		t.Fatalf("negative flow: develop has %s commits, want 1 (something merged!)", count)
	}
	allMsgs := gitT(t, clone, "log", "--format=%B", "develop")
	if strings.Contains(allMsgs, "[task:T-red]") {
		t.Fatalf("negative flow: blocked task's trailer reached develop:\n%s", allMsgs)
	}

	t.Logf("NEGATIVE: ticks=%d finalOutcome=%s reviewResult=%q taskStatus=%s developCommits=%s noTrailer=ok",
		ticks, last.Outcome, last.Review.Result, got.Status, count)
}

// TestE2E_HumanHold_ApproveMerges_NoReDevelop is the GOVERNANCE flow proof with
// REAL git (Faz-1.5-b): a T3 task whose performer writes passing Go is HELD by the
// human-required policy (no merge, no [task:<id>] trailer, verified branch
// PRESERVED), an operator APPROVES it, and a later tick MERGES the preserved verified
// branch WITHOUT re-developing. The performer writes a hit-count file; we prove it
// stays at 1 — develop ran exactly once across both ticks.
func TestE2E_HumanHold_ApproveMerges_NoReDevelop(t *testing.T) {
	requireGit(t)
	requireGo(t)
	ctx := context.Background()

	upstream := newProductRepo(t)

	store := statestore.NewMemoryStore()
	mustCreateProject(t, store, statestore.Project{ID: e2eProjectID, Repo: upstream, BaseBranch: "develop"})
	mustCreateScenario(t, store, statestore.Scenario{ID: "scn-t3", ProjectID: e2eProjectID, Title: "a high-risk T3 change", HoldoutRef: "e2e-noop"})
	mustCreateTask(t, store, statestore.Task{
		ID: "T-hold", ProjectID: e2eProjectID, Lane: "build", Tier: "T3", // T3 -> human-required
		Status: registry.StatusReady, ScenarioID: "scn-t3",
	})

	// A develop-cmd wrapper that bumps a HIT-COUNT file each invocation, then runs the
	// good performer. The hit-count file proves how many times develop actually ran.
	hitFile := filepath.Join(t.TempDir(), "develop-hits")
	performer := writePerformer(t, hitCountingPerformerScript(hitFile))

	root := t.TempDir()
	cond := buildGovernedConductor(t, store, root, performer)
	clone := filepath.Join(root, "clones", e2eProjectID)

	// --- Tick 1: develop -> verify PASS -> HELD (no merge) --------------------
	res1, err := cond.Tick(ctx, e2eProjectID)
	if err != nil {
		t.Fatalf("tick 1 error: %v", err)
	}
	if res1.Outcome != OutcomeHeld {
		t.Fatalf("tick 1 outcome = %q, want held; res=%+v", res1.Outcome, res1)
	}
	// No [task:<id>] trailer on the base for a held task.
	if msgs := gitT(t, clone, "log", "--format=%B", "develop"); strings.Contains(msgs, "[task:T-hold]") {
		t.Fatalf("tick 1: held task must NOT land a trailer on base; got:\n%s", msgs)
	}
	if c := gitT(t, clone, "rev-list", "--count", "develop"); c != "1" {
		t.Fatalf("tick 1: base advanced to %s commits, want 1 (held = no merge)", c)
	}
	held := mustGetTask(t, store, "T-hold")
	if held.Status != StatusAwaitingApproval {
		t.Fatalf("tick 1: status = %q, want %q", held.Status, StatusAwaitingApproval)
	}
	if held.Branch == "" {
		t.Fatalf("tick 1: held task must record its verified branch")
	}
	// The verified branch survived the held tick's worktree cleanup (the proof the
	// work is preserved, recoverable for the approve-merge).
	if err := gitErr(clone, "rev-parse", "--verify", "refs/heads/"+held.Branch); err != nil {
		t.Fatalf("tick 1: verified branch %q did not survive cleanup: %v", held.Branch, err)
	}
	if got := readHits(t, hitFile); got != 1 {
		t.Fatalf("tick 1: develop ran %d times, want 1", got)
	}

	// --- approve (mirrors `conductorctl approve --project ...`) ----------------
	approvedID, err := NewStoreApprover(store).RequestApprove(ctx, e2eProjectID, "")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if approvedID != "T-hold" {
		t.Fatalf("approved id = %q, want T-hold", approvedID)
	}

	// --- Tick 2: approved -> re-attach preserved branch -> re-verify -> MERGE --
	res2, err := cond.Tick(ctx, e2eProjectID)
	if err != nil {
		t.Fatalf("tick 2 error: %v", err)
	}
	if res2.Outcome != OutcomeApprovedMerged {
		t.Fatalf("tick 2 outcome = %q, want approved-merged; res=%+v", res2.Outcome, res2)
	}
	if res2.MergeSHA == "" {
		t.Fatalf("tick 2: approved-merged but empty MergeSHA")
	}

	// THE PROOF: develop ran exactly ONCE total — the approve merged preserved work.
	if got := readHits(t, hitFile); got != 1 {
		t.Fatalf("develop hit-count = %d after approve-merge, want 1 (approve must NOT re-develop)", got)
	}
	// git truth: the [task:<id>] trailer now lands on the base, the new file landed,
	// and the task is done.
	tip := gitT(t, clone, "log", "-1", "--format=%B", "develop")
	if !strings.Contains(tip, "[task:T-hold]") {
		t.Fatalf("tick 2: base tip missing [task:T-hold] trailer; got:\n%s", tip)
	}
	files := gitT(t, clone, "ls-tree", "-r", "--name-only", "develop")
	if !strings.Contains(files, "greeting.go") {
		t.Fatalf("tick 2: greeting.go did not land on develop; tree:\n%s", files)
	}
	done := mustGetTask(t, store, "T-hold")
	if done.Status != registry.StatusDone {
		t.Fatalf("tick 2: final status = %q, want done", done.Status)
	}

	t.Logf("HUMAN-HOLD: tick1=held(noMerge,branchPreserved=%s) approve=ok tick2=approved-merged mergeSHA=%s developHits=1 trailer=ok done",
		held.Branch, res2.MergeSHA[:min(12, len(res2.MergeSHA))])
}

// TestE2E_ApproveReVerify_SemanticBaseDrift_BlocksMerge proves M1: the approve
// re-verify now brings the ADVANCED base INTO the re-attached preserved branch
// before re-verifying, so a SEMANTIC base drift — a base change that breaks the held
// branch's gate WITHOUT a textual conflict — is honestly caught and the merge is
// REFUSED (approved-rejected, blocked, no [task:<id>] trailer), never fake-green.
//
// Construction of the drift: the held branch adds greeting.go with `func Greeting()`.
// While it is held, the base (develop) advances by adding a DIFFERENT new file,
// dup_greeting.go, that ALSO declares `func Greeting()`. These touch different files
// (no textual merge conflict), but combined they are a duplicate declaration that
// FAILS `go build` — exactly the semantic drift the pre-M1 re-verify (which ran the
// gate on the preserved tip alone, without the base) would have MISSED and only hit
// as... nothing (no textual conflict at SquashMerge either, so it would have merged a
// broken tree). With M1 the base is merged in first, so the gate runs on the combined
// (broken) tree and the merge is refused.
func TestE2E_ApproveReVerify_SemanticBaseDrift_BlocksMerge(t *testing.T) {
	requireGit(t)
	requireGo(t)
	ctx := context.Background()

	upstream := newProductRepo(t)

	store := statestore.NewMemoryStore()
	mustCreateProject(t, store, statestore.Project{ID: e2eProjectID, Repo: upstream, BaseBranch: "develop"})
	mustCreateScenario(t, store, statestore.Scenario{ID: "scn-t3", ProjectID: e2eProjectID, Title: "a high-risk T3 change", HoldoutRef: "e2e-noop"})
	mustCreateTask(t, store, statestore.Task{
		ID: "T-hold", ProjectID: e2eProjectID, Lane: "build", Tier: "T3", // T3 -> human-required
		Status: registry.StatusReady, ScenarioID: "scn-t3",
	})

	performer := writePerformer(t, goodPerformerScript) // adds greeting.go with Greeting()
	root := t.TempDir()
	cond := buildGovernedConductor(t, store, root, performer)
	clone := filepath.Join(root, "clones", e2eProjectID)

	// --- Tick 1: develop -> verify PASS -> HELD (no merge) --------------------
	res1, err := cond.Tick(ctx, e2eProjectID)
	if err != nil {
		t.Fatalf("tick 1 error: %v", err)
	}
	if res1.Outcome != OutcomeHeld {
		t.Fatalf("tick 1 outcome = %q, want held; res=%+v", res1.Outcome, res1)
	}
	held := mustGetTask(t, store, "T-hold")
	if held.Status != StatusAwaitingApproval || held.Branch == "" {
		t.Fatalf("tick 1: held=%+v, want awaiting-approval with a recorded branch", held)
	}

	// --- base drifts SEMANTICALLY while the task is held ----------------------
	// Add a NEW file on the UPSTREAM develop that ALSO declares func Greeting().
	// Different file from the held branch's greeting.go => no textual conflict; but
	// the COMBINED tree has a duplicate declaration => `go build` fails (semantic).
	writeFile(t, filepath.Join(upstream, "dup_greeting.go"),
		"package under\n\n// Greeting is ALSO declared on the advanced base, colliding\n"+
			"// with the held branch's greeting.go (semantic drift, no textual conflict).\n"+
			"func Greeting() string { return \"from-base\" }\n")
	gitT(t, upstream, "add", "dup_greeting.go")
	gitT(t, upstream, "commit", "-q", "-m", "base: add a colliding Greeting (semantic drift)")

	// --- approve (mirrors `conductorctl approve`) -----------------------------
	if _, err := NewStoreApprover(store).RequestApprove(ctx, e2eProjectID, ""); err != nil {
		t.Fatalf("approve: %v", err)
	}

	// --- Tick 2: approve -> merge advanced base in -> re-verify FAILS -> REFUSE
	res2, err := cond.Tick(ctx, e2eProjectID)
	if err != nil {
		t.Fatalf("tick 2 error: %v", err)
	}
	if res2.Outcome != OutcomeApprovedRejected {
		t.Fatalf("tick 2 outcome = %q, want approved-rejected (semantic base drift must block); res=%+v", res2.Outcome, res2)
	}

	// git truth: NO [task:<id>] trailer landed on the base — the merge was refused.
	if msgs := gitT(t, clone, "log", "--format=%B", "develop"); strings.Contains(msgs, "[task:T-hold]") {
		t.Fatalf("approved-rejected must NOT land a trailer on base; got:\n%s", msgs)
	}
	// The task is blocked and its approval cleared (a re-approval is a fresh decision).
	got := mustGetTask(t, store, "T-hold")
	if got.Status != registry.StatusBlocked {
		t.Fatalf("tick 2: status = %q, want blocked", got.Status)
	}
	if got.Approved {
		t.Fatalf("tick 2: rejected task must clear its approval flag")
	}
	t.Logf("M1: held branch + base advanced with a SEMANTIC (no-textual-conflict) collision -> approve -> base merged into branch -> re-verify FAILED -> approved-rejected, no trailer, blocked")
}

// TestE2E_ApproveReVerify_TextualBaseConflict_BlocksMerge proves the M1 base-merge
// CONFLICT path: when bringing the advanced base into the preserved branch conflicts
// TEXTUALLY (both changed the same lines), MergeBaseIntoWorktree aborts the merge and
// the conductor refuses to merge (approved-rejected), honestly — never fake-green and
// never leaving a half-merged worktree.
func TestE2E_ApproveReVerify_TextualBaseConflict_BlocksMerge(t *testing.T) {
	requireGit(t)
	requireGo(t)
	ctx := context.Background()

	upstream := newProductRepo(t)

	store := statestore.NewMemoryStore()
	mustCreateProject(t, store, statestore.Project{ID: e2eProjectID, Repo: upstream, BaseBranch: "develop"})
	mustCreateScenario(t, store, statestore.Scenario{ID: "scn-t3", ProjectID: e2eProjectID, Title: "T3", HoldoutRef: "e2e-noop"})
	mustCreateTask(t, store, statestore.Task{
		ID: "T-hold", ProjectID: e2eProjectID, Lane: "build", Tier: "T3",
		Status: registry.StatusReady, ScenarioID: "scn-t3",
	})

	// A performer that EDITS the existing lib.go (changes Version's return) so the base
	// can later change the SAME line -> textual conflict on merge-base.
	performer := writePerformer(t, editVersionPerformerScript("v-branch"))
	root := t.TempDir()
	cond := buildGovernedConductor(t, store, root, performer)
	clone := filepath.Join(root, "clones", e2eProjectID)

	res1, err := cond.Tick(ctx, e2eProjectID)
	if err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if res1.Outcome != OutcomeHeld {
		t.Fatalf("tick 1 outcome = %q, want held", res1.Outcome)
	}

	// Base changes the SAME line in lib.go -> a textual conflict on merge-base.
	writeFile(t, filepath.Join(upstream, "lib.go"),
		"package under\n\n// Version is the seed package symbol.\nfunc Version() string { return \"v-base\" }\n")
	gitT(t, upstream, "add", "lib.go")
	gitT(t, upstream, "commit", "-q", "-m", "base: change Version (textual conflict with held branch)")

	if _, err := NewStoreApprover(store).RequestApprove(ctx, e2eProjectID, ""); err != nil {
		t.Fatalf("approve: %v", err)
	}

	res2, err := cond.Tick(ctx, e2eProjectID)
	if err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if res2.Outcome != OutcomeApprovedRejected {
		t.Fatalf("tick 2 outcome = %q, want approved-rejected (textual base conflict must block); res=%+v", res2.Outcome, res2)
	}
	if msgs := gitT(t, clone, "log", "--format=%B", "develop"); strings.Contains(msgs, "[task:T-hold]") {
		t.Fatalf("conflict must NOT land a trailer on base; got:\n%s", msgs)
	}
	got := mustGetTask(t, store, "T-hold")
	if got.Status != registry.StatusBlocked || got.Approved {
		t.Fatalf("conflict: task=%+v, want blocked with approval cleared", got)
	}
	t.Logf("M1: textual base conflict on merge-base -> aborted -> approved-rejected, no trailer, blocked")
}

// editVersionPerformerScript returns a performer that REWRITES lib.go's Version()
// return value (touching the same line the base may later change, to force a textual
// conflict), commits it, and emits a schema-valid pass Verdict. The package still
// compiles + tests pass on the branch alone, so it is HELD (T3) cleanly.
func editVersionPerformerScript(val string) string {
	return `#!/bin/sh
set -e
cat > lib.go <<'EOF'
package under

// Version is the seed package symbol.
func Version() string { return "` + val + `" }
EOF
export GIT_AUTHOR_NAME=performer GIT_AUTHOR_EMAIL=performer@local
export GIT_COMMITTER_NAME=performer GIT_COMMITTER_EMAIL=performer@local
git add lib.go
git commit -q -m "feat: change Version on branch"
BR=$(git rev-parse --abbrev-ref HEAD)
SHA=$(git rev-parse HEAD)
cat <<EOF
{"result":"pass","branch":"$BR","commit_sha":"$SHA","checks":[{"name":"local","result":"pass","evidence":"committed"}],"files":["lib.go"],"summary":"changed Version"}
EOF
`
}

// buildGovernedConductor wires the REAL components like buildConductor but ALSO
// injects the human-required governance policy (so T3/T4 holds) and the store-backed
// approver + re-attach-capable provisioner — the Faz-1.5-b approve flow.
func buildGovernedConductor(t *testing.T, store *statestore.MemoryStore, root, performer string) *Conductor {
	t.Helper()
	prov, err := provisioner.New(provisioner.Config{RootDir: root})
	if err != nil {
		t.Fatalf("provisioner.New: %v", err)
	}
	eng := engine.NewCommandEngine(engine.RecipeConfig{DevelopCmd: []string{performer}, Timeout: 60 * time.Second})
	verf := verify.New(noopHoldout{}, verify.Config{HoldoutCmd: []string{"true"}})
	merger := NewGitMerger(func(projectID string) string { return filepath.Join(root, "clones", projectID) })

	cond, err := New(Deps{
		Store:       store,
		Picker:      registry.NewRegistry(store),
		Provisioner: prov,
		Engine:      eng,
		Verifier:    verf,
		Merger:      merger,
		Recipe: Recipe{Gates: []verify.Gate{
			{Name: "go build", Argv: []string{"go", "build", "./..."}},
			{Name: "go test", Argv: []string{"go", "test", "./..."}},
		}},
		HostID:   "e2e-host",
		Policy:   governance.DefaultPolicy(),
		Approver: NewStoreApprover(store),
	})
	if err != nil {
		t.Fatalf("conductor.New: %v", err)
	}
	return cond
}

// hitCountingPerformerScript wraps the good performer with a hit-counter: it appends
// a line to hitFile each time develop runs, so the test can prove develop ran exactly
// once (approve must NOT re-roll develop).
func hitCountingPerformerScript(hitFile string) string {
	return "#!/bin/sh\nset -e\necho hit >> " + shellQuote(hitFile) + "\n" + goodPerformerScript[len("#!/bin/sh\n"):]
}

// shellQuote single-quotes a path for safe embedding in the /bin/sh performer.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// readHits returns the number of times the hit-counting performer ran (lines in
// hitFile); a missing file means zero.
func readHits(t *testing.T, hitFile string) int {
	t.Helper()
	b, err := os.ReadFile(hitFile)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read hit file: %v", err)
	}
	return strings.Count(string(b), "hit\n")
}

// gitErr runs a git command and returns its error (used for branch-existence checks).
func gitErr(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.Run()
}

// --- wiring helpers ---------------------------------------------------------

// buildConductor wires the REAL components (provisioner, verifier, registry,
// merger, product CommandEngine) into a Conductor for the e2e scenario. The only
// non-default piece is the engine's performer command, supplied per test.
func buildConductor(t *testing.T, store *statestore.MemoryStore, root, performer string) *Conductor {
	t.Helper()

	prov, err := provisioner.New(provisioner.Config{RootDir: root})
	if err != nil {
		t.Fatalf("provisioner.New: %v", err)
	}

	// REAL product engine: CommandEngine runs the performer subprocess in the
	// worktree and parses its stdout into a Verdict with the product's own code.
	eng := engine.NewCommandEngine(engine.RecipeConfig{
		DevelopCmd: []string{performer},
		Timeout:    60 * time.Second,
	})

	// REAL verifier: deterministic go build + go test gates over the worktree.
	verf := verify.New(noopHoldout{}, verify.Config{
		HoldoutCmd: []string{"true"}, // inert holdout runner; gate decision is the build/test.
	})

	// REAL merger, resolving the clone the same way the provisioner laid it out.
	merger := NewGitMerger(func(projectID string) string {
		return filepath.Join(root, "clones", projectID)
	})

	cond, err := New(Deps{
		Store:       store,
		Picker:      registry.NewRegistry(store),
		Provisioner: prov,
		Engine:      eng,
		Verifier:    verf,
		Merger:      merger,
		Recipe: Recipe{Gates: []verify.Gate{
			{Name: "go build", Argv: []string{"go", "build", "./..."}},
			{Name: "go test", Argv: []string{"go", "test", "./..."}},
		}},
		HostID: "e2e-host",
	})
	if err != nil {
		t.Fatalf("conductor.New: %v", err)
	}
	return cond
}

// --- the product-under-conductor fixture ------------------------------------

// newProductRepo creates a throwaway local git repo: a minimal Go module with one
// package and an initial commit, on a `develop` branch. The provisioner clones
// THIS (by filesystem path) for real. Returns the absolute repo path.
func newProductRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	gitT(t, dir, "init", "-q")
	gitT(t, dir, "config", "user.name", "e2e")
	gitT(t, dir, "config", "user.email", "e2e@local")
	gitT(t, dir, "checkout", "-q", "-b", "develop")

	writeFile(t, filepath.Join(dir, "go.mod"), "module example.com/under-conductor\n\ngo 1.26\n")
	writeFile(t, filepath.Join(dir, "lib.go"), "package under\n\n// Version is the seed package symbol.\nfunc Version() string { return \"v0\" }\n")
	writeFile(t, filepath.Join(dir, "lib_test.go"), "package under\n\nimport \"testing\"\n\nfunc TestVersion(t *testing.T) {\n\tif Version() == \"\" {\n\t\tt.Fatal(\"empty version\")\n\t}\n}\n")

	gitT(t, dir, "add", ".")
	gitT(t, dir, "commit", "-q", "-m", "seed: under-conductor module")
	return dir
}

// goodPerformerScript writes a NEW compiling, test-passing Go file + test into the
// worktree, commits it on the per-task branch, and emits a schema-valid Verdict
// JSON on stdout. $PWD is the worktree (CommandEngine runs it there). It reads the
// branch name out of git so the Verdict.Branch is real.
const goodPerformerScript = `#!/bin/sh
set -e
cat > greeting.go <<'EOF'
package under

// Greeting returns a fixed greeting; added by the e2e performer.
func Greeting() string { return "hello" }
EOF
cat > greeting_test.go <<'EOF'
package under

import "testing"

func TestGreeting(t *testing.T) {
	if Greeting() != "hello" {
		t.Fatalf("got %q", Greeting())
	}
}
EOF
export GIT_AUTHOR_NAME=performer GIT_AUTHOR_EMAIL=performer@local
export GIT_COMMITTER_NAME=performer GIT_COMMITTER_EMAIL=performer@local
git add greeting.go greeting_test.go
git commit -q -m "feat: add Greeting helper"
BR=$(git rev-parse --abbrev-ref HEAD)
SHA=$(git rev-parse HEAD)
cat <<EOF
{"result":"pass","branch":"$BR","commit_sha":"$SHA","checks":[{"name":"local","result":"pass","evidence":"committed"}],"files":["greeting.go","greeting_test.go"],"summary":"added Greeting"}
EOF
`

// badPerformerScript writes Go that does NOT compile (references an undefined
// symbol), commits it, and STILL self-reports result=pass in its Verdict — a
// deliberately lying performer. The independent verify gate (go build/test) must
// catch it and block the merge (Rule#9).
const badPerformerScript = `#!/bin/sh
set -e
cat > broken.go <<'EOF'
package under

// Broken references an undefined symbol so the package does not compile.
func Broken() string { return doesNotExist() }
EOF
export GIT_AUTHOR_NAME=performer GIT_AUTHOR_EMAIL=performer@local
export GIT_COMMITTER_NAME=performer GIT_COMMITTER_EMAIL=performer@local
git add broken.go
git commit -q -m "feat: add Broken (does not compile)"
BR=$(git rev-parse --abbrev-ref HEAD)
SHA=$(git rev-parse HEAD)
cat <<EOF
{"result":"pass","branch":"$BR","commit_sha":"$SHA","checks":[{"name":"local","result":"pass","evidence":"committed"}],"files":["broken.go"],"summary":"added Broken (lying: claims pass)"}
EOF
`

// writePerformer writes the script into a temp file, makes it executable, and
// returns its absolute path for use as the engine's DevelopCmd.
func writePerformer(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("e2e performer is a /bin/sh script; unsupported on windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "performer.sh")
	writeFile(t, path, script)
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod performer: %v", err)
	}
	return path
}

// --- small assertion / fixture helpers --------------------------------------

func mustCreateProject(t *testing.T, s *statestore.MemoryStore, p statestore.Project) {
	t.Helper()
	if err := s.CreateProject(context.Background(), p); err != nil {
		t.Fatalf("create project: %v", err)
	}
}

func mustCreateTask(t *testing.T, s *statestore.MemoryStore, task statestore.Task) {
	t.Helper()
	if err := s.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}
}

func mustCreateScenario(t *testing.T, s *statestore.MemoryStore, sc statestore.Scenario) {
	t.Helper()
	if err := s.CreateScenario(context.Background(), sc); err != nil {
		t.Fatalf("create scenario: %v", err)
	}
}

func mustGetTask(t *testing.T, s *statestore.MemoryStore, id string) statestore.Task {
	t.Helper()
	task, err := s.GetTask(context.Background(), id)
	if err != nil {
		t.Fatalf("get task %q: %v", id, err)
	}
	return task
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

func requireGo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
}
