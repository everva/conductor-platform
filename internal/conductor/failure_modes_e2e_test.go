//go:build e2e

// Package conductor e2e (failure modes): full-pipeline (conductor.Tick) coverage
// proving that each REAL-claude failure mode the product's CommandEngine
// classifies (ADR-0014) produces the correct END-TO-END outcome — a blocked /
// stopped task with NO merge — not merely an engine-level error (N-6 Part A).
//
// The engine-level mapping (malformed -> ErrMalformedVerdict, auth-wall ->
// ErrAuthExpired, timeout/empty -> ErrNoVerdict) is already unit-tested in
// internal/engine/command_engine_test.go. This file builds ON that at the tick
// level: it runs a FULL conductor.Tick through the product's REAL CommandEngine
// (no runner stub on the engine seam), only swapping the performer subprocess
// for a tiny crafted shell script that emits the SAME stdout shape a real
// `claude -p` would in each failure mode. The git/provision/verify/merge spine
// is the genuine article (it reuses the wiring + product-repo helpers from
// e2e_test.go in this same package).
//
// The never-fake-green proof is identical to the negative flow: develop's tip is
// asserted to still be the onboard baseline with NO [task:<id>] trailer for every
// blocked/stopped case. Run with:
//
//	go test -tags e2e -run E2E -v ./internal/conductor/
package conductor

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/provisioner"
	"github.com/everva/conductor-platform/internal/registry"
	"github.com/everva/conductor-platform/internal/statestore"
	"github.com/everva/conductor-platform/internal/verify"
)

// --- claude-CLI-SHAPED failure-mode performer scripts -----------------------
//
// Each script emits the stdout shape a real `claude -p` would in that mode, so
// the product's own classifyOutput/ParseVerdict consumes it (the stubs match the
// EXACT markers command_engine.go keys on: the auth markers in `authMarkers`, the
// last result-keyed JSON object for the verdict, empty/slow output for no-verdict).

// malformedPerformerScript narrates like claude but emits NO parseable verdict
// block: prose plus a result-LESS JSON object (metadata only). ParseVerdict must
// reject it (ErrMalformedVerdict) — never coerce the "all gates passed" prose
// into a passing Verdict (Rule#9). It deliberately does NOT commit anything, so
// even if classification were bypassed there is nothing to merge.
const malformedPerformerScript = `#!/bin/sh
echo "I analyzed the task and implemented the helper."
echo "Ran go build: ok. Ran go test: ok. All gates passed."
echo 'Here is the metadata: {"branch":"conductor/builder/T-malformed","files":["greeting.go"],"summary":"done"}'
echo "Everything looks great!"
`

// authWallPerformerScript prints the auth-wall marker a logged-out `claude -p`
// emits ("Not logged in" / "Please run /login"), which detectAuthMarker matches
// case-insensitively -> ErrAuthExpired. It even appends a JSON verdict to prove
// the auth marker WINS regardless of any later content (the tick must STOP, not
// merge, not block-as-needs-rework).
const authWallPerformerScript = `#!/bin/sh
echo "Error: Not logged in."
echo "Please run /login to authenticate."
echo '{"result":"pass","branch":"x","summary":"this verdict must be ignored"}'
`

// timeoutPerformerScript sleeps well beyond the tight per-develop timeout the
// failure-mode conductor sets, producing NO verdict before the engine's ctx
// deadline fires -> context.DeadlineExceeded -> ErrNoVerdict. It emits nothing on
// stdout so the timeout (not a parse error) is the cause.
const timeoutPerformerScript = `#!/bin/sh
sleep 5
echo '{"result":"pass","summary":"never reached before the deadline"}'
`

// TestE2E_FailureMode_MalformedVerdict_Blocks: a real-claude-shaped run that
// emits prose + a result-less object (no verdict) drives the ErrMalformedVerdict
// path to a BLOCKED task with NO merge.
func TestE2E_FailureMode_MalformedVerdict_Blocks(t *testing.T) {
	requireGit(t)
	requireGo(t)
	assertFailureModeBlocks(t, failureCase{
		taskID:   "T-malformed",
		scenario: "scn-malformed",
		title:    "performer emits no parseable verdict (malformed)",
		script:   malformedPerformerScript,
		timeout:  60 * time.Second,
		want:     OutcomeBlocked,
	})
}

// TestE2E_FailureMode_AuthWall_Stops: a logged-out-shaped run hits the auth wall
// -> ErrAuthExpired -> the tick STOPS (needs-auth), NO merge, and the task is
// NOT mutated to done (surfaced as stopped for a human to re-auth, not fake-green).
func TestE2E_FailureMode_AuthWall_Stops(t *testing.T) {
	requireGit(t)
	requireGo(t)
	assertFailureModeBlocks(t, failureCase{
		taskID:   "T-auth",
		scenario: "scn-auth",
		title:    "performer hits an auth wall (Not logged in)",
		script:   authWallPerformerScript,
		timeout:  60 * time.Second,
		want:     OutcomeStopped,
	})
}

// TestE2E_FailureMode_Timeout_Blocks: a performer that sleeps past the tight
// per-develop timeout yields no verdict before the deadline -> ErrNoVerdict ->
// BLOCKED, NO merge.
func TestE2E_FailureMode_Timeout_Blocks(t *testing.T) {
	requireGit(t)
	requireGo(t)
	assertFailureModeBlocks(t, failureCase{
		taskID:   "T-timeout",
		scenario: "scn-timeout",
		title:    "performer exceeds the per-develop timeout",
		script:   timeoutPerformerScript,
		timeout:  2 * time.Second, // tight: the stub sleeps 30s.
		want:     OutcomeBlocked,
	})
}

// failureCase parameterizes one full-pipeline failure-mode scenario.
type failureCase struct {
	taskID   string
	scenario string
	title    string
	script   string
	timeout  time.Duration
	want     Outcome
}

// assertFailureModeBlocks runs ONE full conductor.Tick through the product's real
// CommandEngine with the crafted failure-mode performer and asserts the terminal
// outcome plus the never-fake-green git/ledger truth: NO merge, the develop tip
// unchanged (still the single onboard baseline commit, no [task:<id>] trailer),
// and the task lifecycle consistent with the outcome (blocked, or untouched on a
// stop). It mirrors the negative flow's proof in e2e_test.go.
func assertFailureModeBlocks(t *testing.T, fc failureCase) {
	t.Helper()
	ctx := context.Background()

	upstream := newProductRepo(t)

	store := statestore.NewMemoryStore()
	mustCreateProject(t, store, statestore.Project{
		ID:         e2eProjectID,
		Repo:       upstream,
		BaseBranch: "develop",
	})
	mustCreateScenario(t, store, statestore.Scenario{
		ID:         fc.scenario,
		ProjectID:  e2eProjectID,
		Title:      fc.title,
		HoldoutRef: "e2e-noop",
	})
	mustCreateTask(t, store, statestore.Task{
		ID:         fc.taskID,
		ProjectID:  e2eProjectID,
		Lane:       "build",
		Tier:       "T2",
		Status:     registry.StatusReady,
		ScenarioID: fc.scenario,
	})

	performer := writePerformer(t, fc.script)
	root := t.TempDir()
	cond := buildFailureModeConductor(t, store, root, performer, fc.timeout)
	clone := filepath.Join(root, "clones", e2eProjectID)

	res, err := cond.Tick(ctx, e2eProjectID)

	// The failure modes surface a wrapped engine sentinel as the tick error
	// (handleDevelopError / verify-failed paths return both an outcome AND the
	// error). The OUTCOME is the contract we assert; a non-nil error is expected.
	if res.Outcome != fc.want {
		t.Fatalf("%s: outcome = %q, want %q (err=%v verdict=%+v review=%+v)",
			fc.title, res.Outcome, fc.want, err, res.Verdict, res.Review)
	}
	if err == nil {
		t.Fatalf("%s: expected a wrapped engine error on the failed tick, got nil", fc.title)
	}

	// NEVER a merge.
	if res.Outcome == OutcomeMerged || res.MergeSHA != "" {
		t.Fatalf("%s: a failure-mode tick MERGED (Rule#9 violated): %+v", fc.title, res)
	}

	// git truth: develop is still exactly the onboard baseline (1 commit), and the
	// task's trailer never reached it.
	count := gitT(t, clone, "rev-list", "--count", "develop")
	if count != "1" {
		t.Fatalf("%s: develop has %s commits, want 1 (something merged!)", fc.title, count)
	}
	trailer := "[task:" + fc.taskID + "]"
	allMsgs := gitT(t, clone, "log", "--format=%B", "develop")
	if strings.Contains(allMsgs, trailer) {
		t.Fatalf("%s: blocked/stopped task's trailer %q reached develop:\n%s", fc.title, trailer, allMsgs)
	}

	// statestore truth, per outcome:
	//   - blocked  -> the task is terminally blocked (never done).
	//   - stopped  -> the auth wall leaves the task lifecycle UNTOUCHED (still
	//     ready) so a human re-auths and the next tick re-picks it (ADR-0014); it
	//     must NOT be done and must NOT be (incorrectly) blocked.
	got := mustGetTask(t, store, fc.taskID)
	switch fc.want {
	case OutcomeBlocked:
		if got.Status != registry.StatusBlocked {
			t.Fatalf("%s: task status = %q, want %q", fc.title, got.Status, registry.StatusBlocked)
		}
	case OutcomeStopped:
		if got.Status == registry.StatusDone {
			t.Fatalf("%s: auth-wall tick fake-greened the task to done", fc.title)
		}
		if got.Status != registry.StatusReady {
			t.Fatalf("%s: auth-wall tick must leave task ready for re-auth, got %q", fc.title, got.Status)
		}
	}

	t.Logf("%s: outcome=%s taskStatus=%s developCommits=%s noTrailer=ok noMerge=ok err=%v",
		fc.title, res.Outcome, got.Status, count, err)
}

// buildFailureModeConductor mirrors buildConductor (e2e_test.go) but lets each
// failure-mode test set the engine's per-develop timeout (the timeout case needs
// a tight one). The verify gates / merger / provisioner are the same REAL pieces.
func buildFailureModeConductor(t *testing.T, store *statestore.MemoryStore, root, performer string, timeout time.Duration) *Conductor {
	t.Helper()

	prov, err := provisioner.New(provisioner.Config{RootDir: root})
	if err != nil {
		t.Fatalf("provisioner.New: %v", err)
	}

	eng := engine.NewCommandEngine(engine.RecipeConfig{
		DevelopCmd: []string{performer},
		Timeout:    timeout,
	})

	verf := verify.New(noopHoldout{}, verify.Config{
		HoldoutCmd: []string{"true"},
	})

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
