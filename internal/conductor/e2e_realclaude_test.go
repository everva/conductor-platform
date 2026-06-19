//go:build e2e && realclaude

// Package conductor real-claude FULL-LOOP e2e: the opt-in counterpart of the
// deterministic-script e2e (TestE2E_OnboardDevelopVerifyMerge), but with the REAL
// `claude -p` CLI as the develop performer. It proves the WHOLE conductor loop end
// to end with no stub on the brain: onboard -> develop(real claude) -> independent
// verify gate (go build / go test) -> squash-merge into develop with the
// [task:<id>] trailer -> task done, AND that the 4C-1 bounded KindDiff event
// (ADR-0030) is emitted on the green gate carrying the changed file.
//
// What is REAL here (everything; only the unit/e2e SCRIPT performer is replaced by
// the genuine CLI): the throwaway local product repo (newProductRepo), the real
// provisioner worktree, the real verifier gates, the real GitMerger, the real
// registry Picker + in-memory store, the real conductor.Tick — and the real
// GitDiffer computing the branch-vs-base diff that is emitted as a KindDiff event.
//
// HARD GUARDS (the default + e2e gates MUST stay deterministic + offline):
//   - build tag `e2e && realclaude` — this file compiles ONLY under
//     `-tags "e2e realclaude"`, so it reuses e2e_test.go's helpers (which are
//     `//go:build e2e`) while staying excluded from `go test ./...` AND from CI's
//     `-tags e2e` alone;
//   - env guard `CP_REAL_CLAUDE=1` — even with the tags, the test SKIPS unless set;
//   - subscription auth ONLY: no API key is set, no --api-key is passed. It relies
//     on the operator's already-logged-in `claude`. No secret is written or logged.
//
// Run it explicitly:
//
//	CP_REAL_CLAUDE=1 CP_CLAUDE_BIN=/Users/you/.local/bin/claude \
//	  go test -tags "e2e realclaude" -run RealClaude -v ./internal/conductor/
package conductor

import (
	"context"
	"os"
	"os/exec"
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

// realClaudeDevelopPrompt is the develop instruction handed to `claude -p`. Its cwd
// IS the per-task worktree (CommandEngine runs the performer there), so it works in
// the CURRENT working directory. It dictates the EXACT file contents the
// deterministic goodPerformerScript writes (so the independent go build/test gate
// behaves identically), demands a real build+test+commit, and ends with ONE
// single-line JSON verdict in the EXACT shape the product's ParseVerdict accepts
// (the LAST top-level JSON object carrying a non-empty "result"; mirrors
// goodPerformerScript's emitted JSON).
const realClaudeDevelopPrompt = `You are a non-interactive build performer. Your current working directory is a git worktree of a small Go module (package "under"). Do EXACTLY this, in the CURRENT WORKING DIRECTORY, and nothing else:

1. Create a file named greeting.go with EXACTLY this content:
package under

// Greeting returns a fixed greeting.
func Greeting() string { return "hello" }

2. Create a file named greeting_test.go with EXACTLY this content:
package under

import "testing"

func TestGreeting(t *testing.T) {
	if Greeting() != "hello" {
		t.Fatalf("got %q", Greeting())
	}
}

3. Run: go build ./... && go test ./...   (both MUST pass).
4. Stage and commit BOTH files on the CURRENT git branch with the message: feat: add Greeting helper

Then, as the VERY LAST thing you output, print ONE single-line JSON object and NOTHING after it, in exactly this schema (no markdown fence, no trailing prose):
{"result":"pass","branch":"<current git branch>","commit_sha":"<the commit sha>","checks":[{"name":"go test","result":"pass","evidence":"go test ./... passed"}],"files":["greeting.go","greeting_test.go"],"summary":"added Greeting helper"}
The "result" value MUST be the literal string "pass" only if go build AND go test passed and you committed both files; otherwise it MUST be "fail".`

// TestE2E_RealClaude_OnboardDevelopVerifyMerge is the POSITIVE full-loop flow with
// the REAL claude CLI as the develop performer: claude writes compiling,
// test-passing Go and commits it; the independent verify gate passes; the branch is
// squash-merged onto develop with a [task:<id>] trailer; the task ends done; and the
// 4C-1 bounded KindDiff event is emitted on the green gate carrying greeting.go.
func TestE2E_RealClaude_OnboardDevelopVerifyMerge(t *testing.T) {
	if os.Getenv("CP_REAL_CLAUDE") != "1" {
		t.Skip("real claude full-loop e2e is opt-in: set CP_REAL_CLAUDE=1")
	}
	requireGit(t)
	requireGo(t)

	claudeBin := os.Getenv("CP_CLAUDE_BIN")
	if claudeBin == "" {
		claudeBin = "claude"
	}
	if _, err := exec.LookPath(claudeBin); err != nil {
		// Fall back to the absolute path if a bare name is not on PATH.
		if _, statErr := os.Stat(claudeBin); statErr != nil {
			t.Skipf("claude binary %q not found: %v", claudeBin, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 360*time.Second)
	defer cancel()

	// --- the throwaway product-under-conductor: a real local git repo (reuse) ----
	upstream := newProductRepo(t)

	// --- onboard: project + a ready task + its scenario into the statestore -------
	store := statestore.NewMemoryStore()
	mustCreateProject(t, store, statestore.Project{
		ID:         e2eProjectID,
		Repo:       upstream, // a local path: the provisioner clones it for real.
		BaseBranch: "develop",
	})
	mustCreateScenario(t, store, statestore.Scenario{
		ID:         "scn-rc",
		ProjectID:  e2eProjectID,
		Title:      "add a Greeting helper with a passing test (real claude)",
		HoldoutRef: "e2e-noop",
	})
	mustCreateTask(t, store, statestore.Task{
		ID:         "T-rc-green",
		ProjectID:  e2eProjectID,
		Lane:       "build",
		Tier:       "T2", // auto-merge tier (Policy is nil) — mirrors the positive test.
		Status:     registry.StatusReady,
		ScenarioID: "scn-rc",
	})

	// REAL claude as the develop performer: subscription auth only (no API key, no
	// --api-key). The capturing emitter (reused recordingEmitter) + the real
	// GitDiffer wire the 4C-1 KindDiff so we can assert it on the green gate.
	developArgv := []string{claudeBin, "-p", realClaudeDevelopPrompt, "--dangerously-skip-permissions"}
	em := &recordingEmitter{}

	root := t.TempDir()
	cond := buildRealClaudeConductor(t, store, root, developArgv, em)

	res, err := cond.Tick(ctx, e2eProjectID)
	if err != nil {
		t.Fatalf("real-claude tick returned error: %v (outcome=%s review=%+v verdict=%+v)",
			err, res.Outcome, res.Review, res.Verdict)
	}

	// --- assert the pipeline reached a real merge --------------------------------
	if res.Outcome != OutcomeMerged {
		t.Fatalf("real-claude flow: want outcome %q, got %q (review=%+v verdict=%+v)",
			OutcomeMerged, res.Outcome, res.Review, res.Verdict)
	}
	if res.MergeSHA == "" {
		t.Fatalf("real-claude flow: merged outcome but empty MergeSHA")
	}

	// statestore truth: the task is done.
	got := mustGetTask(t, store, "T-rc-green")
	if got.Status != registry.StatusDone {
		t.Fatalf("real-claude flow: task status = %q, want %q", got.Status, registry.StatusDone)
	}

	// git truth: develop's tip carries the [task:<id>] trailer AND the new file.
	clone := filepath.Join(root, "clones", e2eProjectID)
	tip := gitT(t, clone, "log", "-1", "--format=%B", "develop")
	if !strings.Contains(tip, "[task:T-rc-green]") {
		t.Fatalf("real-claude flow: develop tip missing [task:T-rc-green] trailer; got:\n%s", tip)
	}
	files := gitT(t, clone, "ls-tree", "-r", "--name-only", "develop")
	if !strings.Contains(files, "greeting.go") {
		t.Fatalf("real-claude flow: greeting.go did not land on develop; tree:\n%s", files)
	}

	// 4C-1 truth (ADR-0030): exactly the KindDiff event fired on the green gate, and
	// its bounded DiffSummary payload lists greeting.go among the changed files with a
	// non-empty patch. diffEvents()/recordingEmitter are reused from the package's
	// existing (no-build-tag) test helpers.
	diffs := em.diffEvents()
	if len(diffs) == 0 {
		t.Fatalf("real-claude flow: no KindDiff event emitted on the green gate; kinds=%v", em.kinds())
	}
	dp := diffs[0].Payload
	patch, _ := dp["patch"].(string)
	if strings.TrimSpace(patch) == "" {
		t.Fatalf("real-claude flow: KindDiff payload has an empty patch; payload=%+v", dp)
	}
	diffFiles, _ := dp["files"].([]any)
	if !diffFilesContain(diffFiles, "greeting.go") {
		t.Fatalf("real-claude flow: KindDiff payload files do not list greeting.go; files=%+v", diffFiles)
	}

	t.Logf("REAL-CLAUDE: outcome=%s mergeSHA=%s taskStatus=%s developTrailer=ok greeting.go landed; KindDiff emitted (files=%d, patch=%dB)",
		res.Outcome, res.MergeSHA[:min(12, len(res.MergeSHA))], got.Status, len(diffFiles), len(patch))
}

// diffFilesContain reports whether the DiffSummary.Payload() "files" list (a []any
// of map[string]any, the wire shape) contains an entry whose "path" equals name.
func diffFilesContain(files []any, name string) bool {
	for _, f := range files {
		m, ok := f.(map[string]any)
		if !ok {
			continue
		}
		if p, _ := m["path"].(string); p == name {
			return true
		}
	}
	return false
}

// buildRealClaudeConductor mirrors buildConductor (the same REAL provisioner,
// verifier, registry, merger, gates, host id, noopHoldout, and nil Policy so a T2
// task auto-merges) but (a) drives the engine with the real-claude develop argv
// instead of a script performer, (b) uses a LONGER engine timeout (the CLI is far
// slower than a shell script), and (c) ALSO wires the capturing Emitter + a real
// GitDiffer so the 4C-1 KindDiff is computed and captured. It does NOT modify
// buildConductor.
func buildRealClaudeConductor(t *testing.T, store *statestore.MemoryStore, root string, developArgv []string, em Emitter) *Conductor {
	t.Helper()

	prov, err := provisioner.New(provisioner.Config{RootDir: root})
	if err != nil {
		t.Fatalf("provisioner.New: %v", err)
	}

	// REAL product engine driving the REAL claude CLI in the worktree; its stdout is
	// parsed into a Verdict by the product's own ParseVerdict (ADR-0014). The timeout
	// is generous because the CLI is much slower than the deterministic script.
	eng := engine.NewCommandEngine(engine.RecipeConfig{
		DevelopCmd: developArgv,
		Timeout:    300 * time.Second,
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
		// 4C-1 (ADR-0030): wire the capturing Emitter + the real GitDiffer so the
		// bounded KindDiff is emitted on the green gate. Policy is left nil so a T2
		// task auto-merges, exactly like buildConductor.
		Emitter: em,
		Differ:  NewGitDiffer(),
	})
	if err != nil {
		t.Fatalf("conductor.New: %v", err)
	}
	return cond
}
