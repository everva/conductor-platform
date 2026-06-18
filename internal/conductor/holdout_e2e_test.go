//go:build e2e

// Package conductor e2e (hidden holdout, A.1): the CORE-guarantee proof that the
// daemon could NOT do before this task — a task whose VISIBLE recipe gates (go
// build / go test) PASS but whose injected, REPO-EXTERNAL HIDDEN holdout FAILS is
// independently driven to changes-requested, so the tick does NOT merge and the
// task ends BLOCKED (Rule#9, ADR-0018). It uses the real provisioner, the real
// CommandEngine performer, the real GitMerger, AND the real filesystem-backed
// holdout.FSStore wired into the real verify gate. Deterministic + offline: the
// holdout is a Go test that always fails; no live claude, no network.
package conductor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/holdout"
	"github.com/everva/conductor-platform/internal/provisioner"
	"github.com/everva/conductor-platform/internal/registry"
	"github.com/everva/conductor-platform/internal/statestore"
	"github.com/everva/conductor-platform/internal/verify"
)

// TestE2E_HiddenHoldoutFailBlocksMerge is the live-negative A.1 closes: the
// performer writes a NEW compiling, test-passing Go file (so the visible go
// build/test gates are GREEN), but the scenario's repo-external hidden holdout —
// resolved by the real FSStore from a root OUTSIDE the product repo — injects a Go
// test that always fails. The independent verify gate therefore returns
// changes-requested, the tick does NOT merge, and the task ends blocked. We prove
// no fake-green: develop's tip is unchanged from the onboard baseline and the
// holdout file never leaks into the develop clone.
func TestE2E_HiddenHoldoutFailBlocksMerge(t *testing.T) {
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
	// The scenario points at a REPO-EXTERNAL hidden holdout (ADR-0018 store:// scheme).
	mustCreateScenario(t, store, statestore.Scenario{
		ID:         "scn-holdout",
		ProjectID:  e2eProjectID,
		Title:      "visible green, hidden holdout fails",
		HoldoutRef: "store://holdouts/HX/spec.yaml",
	})
	mustCreateTask(t, store, statestore.Task{
		ID:         "T-holdout",
		ProjectID:  e2eProjectID,
		Lane:       "build",
		Tier:       "T2",
		Status:     registry.StatusReady,
		ScenarioID: "scn-holdout",
	})

	// --- the REPO-EXTERNAL holdout root (a temp dir, never inside the product) ---
	holdoutRoot := t.TempDir()
	injectDir := filepath.Join(holdoutRoot, "holdouts", "HX", "inject")
	if err := os.MkdirAll(injectDir, 0o755); err != nil {
		t.Fatalf("mkdir holdout inject: %v", err)
	}
	// The hidden holdout: a Go test in the SAME package that ALWAYS fails. Injected
	// into the throwaway verify-worktree, it makes `go test ./...` there exit
	// non-zero — even though the performer's own visible test passes.
	holdoutTest := "package under\n\nimport \"testing\"\n\n" +
		"func TestHiddenHoldout(t *testing.T) {\n\tt.Fatal(\"hidden holdout always fails\")\n}\n"
	if err := os.WriteFile(filepath.Join(injectDir, "hidden_holdout_test.go"), []byte(holdoutTest), 0o644); err != nil {
		t.Fatalf("write holdout test: %v", err)
	}

	performer := writePerformer(t, goodPerformerScript) // writes a PASSING visible test.
	root := t.TempDir()
	cond := buildConductorWithHoldout(t, store, root, performer, holdoutRoot)

	// --- tick 1: independent verify requests changes; the merge gate (Rule#9)
	// rides on the HIDDEN holdout, so NO merge happens. The first changes-requested
	// is a RETRY under the ADR-0004 cap (RetryCount 0 < MaxRetries).
	res, err := cond.Tick(ctx, e2eProjectID)
	if err != nil {
		t.Fatalf("holdout tick returned error: %v (outcome=%s review=%+v)", err, res.Outcome, res.Review)
	}
	if res.Outcome != OutcomeRetry {
		t.Fatalf("hidden holdout fail (tick 1): want outcome %q, got %q (review=%+v verdict=%+v)",
			OutcomeRetry, res.Outcome, res.Review, res.Verdict)
	}
	if res.MergeSHA != "" {
		t.Fatalf("hidden holdout fail: must NOT merge, but got MergeSHA=%q", res.MergeSHA)
	}
	if res.Review.Result != "changes-requested" {
		t.Fatalf("hidden holdout fail: want review changes-requested, got %q (findings=%v)",
			res.Review.Result, res.Review.Findings)
	}
	// The performer self-reported pass; the independent gate overrode it (Rule#9).
	if res.Verdict.Result != "pass" {
		t.Fatalf("expected lying-pass verdict from performer, got %q", res.Verdict.Result)
	}

	// --- exhaust the retry cap: the holdout always fails, so the task never merges
	// and ends BLOCKED (never fake-green). Re-tick until terminal.
	const maxTicks = MaxRetries + 2 // generous bound; loop breaks on terminal outcome.
	for i := 0; i < maxTicks && res.Outcome == OutcomeRetry; i++ {
		res, err = cond.Tick(ctx, e2eProjectID)
		if err != nil {
			t.Fatalf("holdout re-tick %d returned error: %v (outcome=%s)", i, err, res.Outcome)
		}
		if res.MergeSHA != "" {
			t.Fatalf("hidden holdout fail (re-tick %d): must NOT merge, got MergeSHA=%q", i, res.MergeSHA)
		}
	}
	if res.Outcome != OutcomeBlocked {
		t.Fatalf("hidden holdout fail: after retry cap want outcome %q, got %q", OutcomeBlocked, res.Outcome)
	}

	// statestore truth: task blocked (NOT done, never fake-green).
	got := mustGetTask(t, store, "T-holdout")
	if got.Status != registry.StatusBlocked {
		t.Fatalf("hidden holdout fail: task status = %q, want %q", got.Status, registry.StatusBlocked)
	}

	// git truth: develop's tip carries NO [task:<id>] trailer for this task, i.e.
	// nothing landed; and the holdout file never leaked into the develop clone.
	clone := filepath.Join(root, "clones", e2eProjectID)
	tip := gitT(t, clone, "log", "-1", "--format=%B", "develop")
	if strings.Contains(tip, "[task:T-holdout]") {
		t.Fatalf("hidden holdout fail: a [task:T-holdout] trailer landed on develop; tip:\n%s", tip)
	}
	tree := gitT(t, clone, "ls-tree", "-r", "--name-only", "develop")
	if strings.Contains(tree, "hidden_holdout_test.go") {
		t.Fatalf("hidden holdout LEAKED into the develop clone tree:\n%s", tree)
	}
	// Secrecy: holdout file contents must not surface in the findings.
	for _, f := range res.Review.Findings {
		if strings.Contains(f, "always fails") {
			t.Fatalf("holdout contents leaked into findings: %q", f)
		}
	}

	t.Logf("HOLDOUT-NEGATIVE: outcome=%s review=%s taskStatus=%s noMerge=ok noLeak=ok findings=%v",
		res.Outcome, res.Review.Result, got.Status, res.Review.Findings)
}

// buildConductorWithHoldout is buildConductor wired with the REAL filesystem-backed
// holdout.FSStore (rooted at holdoutRoot, repo-external) and `go test ./...` as the
// holdout suite, so the scenario's HoldoutRef is fetched + injected into the verify-
// worktree for real. Everything else (provisioner, CommandEngine, GitMerger, recipe
// gates) matches buildConductor.
func buildConductorWithHoldout(t *testing.T, store *statestore.MemoryStore, root, performer, holdoutRoot string) *Conductor {
	t.Helper()

	prov, err := provisioner.New(provisioner.Config{RootDir: root})
	if err != nil {
		t.Fatalf("provisioner.New: %v", err)
	}
	eng := engine.NewCommandEngine(engine.RecipeConfig{
		DevelopCmd: []string{performer},
		Timeout:    60 * time.Second,
	})

	hstore, err := holdout.New(holdoutRoot)
	if err != nil {
		t.Fatalf("holdout.New: %v", err)
	}
	verf := verify.New(hstore, verify.Config{HoldoutCmd: []string{"go", "test", "./..."}})

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
		HostID: "holdout-e2e-host",
	})
	if err != nil {
		t.Fatalf("conductor.New: %v", err)
	}
	return cond
}
