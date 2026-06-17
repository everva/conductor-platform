package conductor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/registry"
	"github.com/everva/conductor-platform/internal/statestore"
	"github.com/everva/conductor-platform/internal/verify"
)

const projectID = "proj-1"

// --- fakes ------------------------------------------------------------------

// fakeEngine is a scripted engine.EngineAdapter: Develop returns the configured
// verdict/error so the tick can be driven through every resilience path without a
// real `claude -p`. Only Develop is exercised by the tick; the other verbs are
// no-op stubs satisfying the frozen contract.
type fakeEngine struct {
	verdict    engine.Verdict
	developErr error
	developed  int
}

func (f *fakeEngine) Develop(_ context.Context, _ statestore.Task, _ engine.Workspace) (engine.Verdict, error) {
	f.developed++
	if f.developErr != nil {
		return engine.Verdict{}, f.developErr
	}
	return f.verdict, nil
}
func (f *fakeEngine) Verify(context.Context, engine.Verdict, engine.Workspace) (engine.ReviewResult, error) {
	return engine.ReviewResult{}, nil
}
func (f *fakeEngine) Health(context.Context, engine.Session) (engine.HealthState, error) {
	return engine.HealthState{}, nil
}
func (f *fakeEngine) Events(context.Context) (<-chan engine.Event, error) { return nil, nil }
func (f *fakeEngine) Control(context.Context, engine.Command) error       { return nil }

// fakeProvisioner records workspace + cleanup calls; it does no real git so the
// non-merge tests stay hermetic. Path/Branch are synthesized from the task.
type fakeProvisioner struct {
	wsCalls      int
	cleanupCalls int
	ws           engine.Workspace
}

func (f *fakeProvisioner) Workspace(_ context.Context, _ statestore.Project, task statestore.Task) (engine.Workspace, error) {
	f.wsCalls++
	f.ws = engine.Workspace{Path: "/tmp/ws/" + task.ID, Branch: "conductor/" + task.ID}
	return f.ws, nil
}
func (f *fakeProvisioner) Cleanup(_ context.Context, _ engine.Workspace) error {
	f.cleanupCalls++
	return nil
}

// fakeVerifier returns a scripted ReviewResult so the merge gate (Rule#9) can be
// driven independently of the engine's self-reported verdict.
type fakeVerifier struct {
	result string
	err    error
	calls  int
}

func (f *fakeVerifier) Verify(context.Context, engine.Verdict, engine.Workspace, []verify.Gate, string) (engine.ReviewResult, []engine.Check, error) {
	f.calls++
	if f.err != nil {
		return engine.ReviewResult{}, nil, f.err
	}
	return engine.ReviewResult{Result: f.result}, nil, nil
}

// fakeMerger records merge calls and returns a fixed SHA so the green path can be
// asserted without a real repo (a separate test exercises GitMerger for real).
type fakeMerger struct {
	calls int
	sha   string
}

func (f *fakeMerger) SquashMerge(context.Context, statestore.Project, statestore.Task, engine.Workspace) (string, error) {
	f.calls++
	return f.sha, nil
}

// --- harness ----------------------------------------------------------------

type harness struct {
	store *statestore.MemoryStore
	eng   *fakeEngine
	prov  *fakeProvisioner
	verf  *fakeVerifier
	merge *fakeMerger
	cond  *Conductor
}

func newHarness(t *testing.T, verdict engine.Verdict, developErr error, reviewResult string, verifyErr error) *harness {
	t.Helper()
	ctx := context.Background()
	store := statestore.NewMemoryStore()
	if err := store.CreateProject(ctx, statestore.Project{ID: projectID, Repo: "owner/repo", BaseBranch: "develop"}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := store.CreateTask(ctx, statestore.Task{ID: "T-1", ProjectID: projectID, Lane: "x", Tier: "T2", Status: registry.StatusReady, Branch: "conductor/T-1"}); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	h := &harness{
		store: store,
		eng:   &fakeEngine{verdict: verdict, developErr: developErr},
		prov:  &fakeProvisioner{},
		verf:  &fakeVerifier{result: reviewResult, err: verifyErr},
		merge: &fakeMerger{sha: "deadbeef"},
	}
	cond, err := New(Deps{
		Store:       store,
		Picker:      registry.NewRegistry(store),
		Provisioner: h.prov,
		Engine:      h.eng,
		Verifier:    h.verf,
		Merger:      h.merge,
		HostID:      "host-1",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.cond = cond
	return h
}

func (h *harness) task(t *testing.T) statestore.Task {
	t.Helper()
	task, err := h.store.GetTask(context.Background(), "T-1")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	return task
}

func (h *harness) leaseHeld(t *testing.T) bool {
	t.Helper()
	_, err := h.store.GetLease(context.Background(), projectID)
	if err == nil {
		return true
	}
	if errors.Is(err, statestore.ErrNotFound) {
		return false
	}
	t.Fatalf("get lease: %v", err)
	return false
}

// --- tests ------------------------------------------------------------------

func TestConductor_Tick_GreenPath_Merges(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, engine.Verdict{Result: "pass"}, nil, "pass", nil)

	res, err := h.cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if res.Outcome != OutcomeMerged {
		t.Fatalf("outcome = %q, want merged; res=%+v", res.Outcome, res)
	}
	if res.MergeSHA != "deadbeef" {
		t.Fatalf("merge sha = %q, want deadbeef", res.MergeSHA)
	}
	if h.merge.calls != 1 {
		t.Fatalf("merge calls = %d, want 1", h.merge.calls)
	}
	if got := h.task(t).Status; got != registry.StatusDone {
		t.Fatalf("task status = %q, want done", got)
	}
	if h.leaseHeld(t) {
		t.Fatalf("lease must be released after tick")
	}
	if h.prov.cleanupCalls != 1 {
		t.Fatalf("worktree cleanup calls = %d, want 1", h.prov.cleanupCalls)
	}
}

func TestConductor_Tick_HoldoutFail_NoMerge(t *testing.T) {
	ctx := context.Background()
	// Verdict self-reports pass but independent verify requests changes (Rule#9
	// negative): NO merge, task not done.
	h := newHarness(t, engine.Verdict{Result: "pass"}, nil, "changes-requested", nil)

	res, err := h.cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if res.Outcome != OutcomeRetry {
		t.Fatalf("outcome = %q, want retry", res.Outcome)
	}
	if h.merge.calls != 0 {
		t.Fatalf("merge must not be called on changes-requested, got %d", h.merge.calls)
	}
	tk := h.task(t)
	if tk.Status == registry.StatusDone {
		t.Fatalf("task must not be done on changes-requested")
	}
	if tk.RetryCount != 1 {
		t.Fatalf("retry count = %d, want 1", tk.RetryCount)
	}
	if h.leaseHeld(t) {
		t.Fatalf("lease must be released")
	}
	if h.prov.cleanupCalls != 1 {
		t.Fatalf("cleanup calls = %d, want 1", h.prov.cleanupCalls)
	}
}

func TestConductor_Tick_ChangesRequested_RetryCapBlocks(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, engine.Verdict{Result: "pass"}, nil, "changes-requested", nil)
	// Pre-set the task at the retry cap so this tick blocks rather than retries.
	tk := h.task(t)
	tk.RetryCount = MaxRetries
	if err := h.store.UpdateTask(ctx, tk); err != nil {
		t.Fatalf("preset retry: %v", err)
	}

	res, err := h.cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if res.Outcome != OutcomeBlocked {
		t.Fatalf("outcome = %q, want blocked", res.Outcome)
	}
	if got := h.task(t).Status; got != registry.StatusBlocked {
		t.Fatalf("task status = %q, want blocked", got)
	}
	if h.merge.calls != 0 {
		t.Fatalf("merge must not be called, got %d", h.merge.calls)
	}
}

func TestConductor_Tick_MalformedVerdict_Blocks(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, engine.Verdict{}, fmt.Errorf("develop: %w", engine.ErrMalformedVerdict), "", nil)

	res, err := h.cond.Tick(ctx, projectID)
	if !errors.Is(err, engine.ErrMalformedVerdict) {
		t.Fatalf("err = %v, want wrapping ErrMalformedVerdict", err)
	}
	if res.Outcome != OutcomeBlocked {
		t.Fatalf("outcome = %q, want blocked", res.Outcome)
	}
	if got := h.task(t).Status; got != registry.StatusBlocked {
		t.Fatalf("task status = %q, want blocked", got)
	}
	if h.merge.calls != 0 || h.verf.calls != 0 {
		t.Fatalf("no merge/verify after malformed develop; merge=%d verify=%d", h.merge.calls, h.verf.calls)
	}
	if h.leaseHeld(t) {
		t.Fatalf("lease must be released")
	}
	if h.prov.cleanupCalls != 1 {
		t.Fatalf("cleanup must still run, got %d", h.prov.cleanupCalls)
	}
}

func TestConductor_Tick_NoVerdict_Blocks(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, engine.Verdict{}, fmt.Errorf("develop: %w", engine.ErrNoVerdict), "", nil)

	res, err := h.cond.Tick(ctx, projectID)
	if !errors.Is(err, engine.ErrNoVerdict) {
		t.Fatalf("err = %v, want wrapping ErrNoVerdict", err)
	}
	if res.Outcome != OutcomeBlocked {
		t.Fatalf("outcome = %q, want blocked", res.Outcome)
	}
	if got := h.task(t).Status; got != registry.StatusBlocked {
		t.Fatalf("task status = %q, want blocked", got)
	}
}

func TestConductor_Tick_AuthExpired_StopsTick(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, engine.Verdict{}, fmt.Errorf("develop: %w", engine.ErrAuthExpired), "", nil)

	res, err := h.cond.Tick(ctx, projectID)
	if !errors.Is(err, engine.ErrAuthExpired) {
		t.Fatalf("err = %v, want wrapping ErrAuthExpired", err)
	}
	if res.Outcome != OutcomeStopped {
		t.Fatalf("outcome = %q, want stopped", res.Outcome)
	}
	// Auth wall does not touch the task lifecycle (no fake block); a human resumes.
	if got := h.task(t).Status; got != registry.StatusReady {
		t.Fatalf("task status = %q, want unchanged ready", got)
	}
	if h.merge.calls != 0 {
		t.Fatalf("no merge on auth expiry")
	}
	if h.leaseHeld(t) {
		t.Fatalf("lease must be released even on auth-stop")
	}
	if h.prov.cleanupCalls != 1 {
		t.Fatalf("worktree must still be cleaned on auth-stop, got %d", h.prov.cleanupCalls)
	}
}

func TestConductor_Tick_VerifyError_Blocks(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, engine.Verdict{Result: "pass"}, nil, "", errors.New("verify boom"))

	res, err := h.cond.Tick(ctx, projectID)
	if err == nil {
		t.Fatalf("expected error when verify fails")
	}
	if res.Outcome != OutcomeBlocked {
		t.Fatalf("outcome = %q, want blocked", res.Outcome)
	}
	if got := h.task(t).Status; got != registry.StatusBlocked {
		t.Fatalf("task status = %q, want blocked", got)
	}
	if h.merge.calls != 0 {
		t.Fatalf("no merge when verify errors")
	}
}

func TestConductor_Tick_NoReadyTask_NoOp(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, engine.Verdict{Result: "pass"}, nil, "pass", nil)
	// Mark the only task done so PickReady finds nothing.
	tk := h.task(t)
	tk.Status = registry.StatusDone
	if err := h.store.UpdateTask(ctx, tk); err != nil {
		t.Fatalf("mark done: %v", err)
	}

	res, err := h.cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if res.Outcome != OutcomeNoOp {
		t.Fatalf("outcome = %q, want noop", res.Outcome)
	}
	if h.prov.wsCalls != 0 || h.eng.developed != 0 || h.merge.calls != 0 {
		t.Fatalf("no-op tick must touch nothing: ws=%d dev=%d merge=%d", h.prov.wsCalls, h.eng.developed, h.merge.calls)
	}
	if h.leaseHeld(t) {
		t.Fatalf("no lease on a no-op tick")
	}
}

func TestConductor_Tick_DonePersisted_SecondTickNoOp(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, engine.Verdict{Result: "pass"}, nil, "pass", nil)

	if _, err := h.cond.Tick(ctx, projectID); err != nil {
		t.Fatalf("first tick: %v", err)
	}
	res, err := h.cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("second tick: %v", err)
	}
	if res.Outcome != OutcomeNoOp {
		t.Fatalf("second tick outcome = %q, want noop (idempotent)", res.Outcome)
	}
	if h.merge.calls != 1 {
		t.Fatalf("merge must happen exactly once across two ticks, got %d", h.merge.calls)
	}
}

func TestNew_NilDep_Errors(t *testing.T) {
	_, err := New(Deps{})
	if err == nil {
		t.Fatalf("expected error for nil deps")
	}
}

// --- real GitMerger integration over a local throwaway repo -----------------

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestGitMerger_SquashMerge_RealRepo proves the real merger squash-merges a task
// branch into the base with an EXACT [task:<id>] trailer the reconciler matches.
func TestGitMerger_SquashMerge_RealRepo(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "develop")
	gitT(t, repo, "config", "user.name", "test")
	gitT(t, repo, "config", "user.email", "test@test")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write base: %v", err)
	}
	gitT(t, repo, "add", ".")
	gitT(t, repo, "commit", "-q", "-m", "base")

	// Cut a task branch with one extra commit, mirroring the develop worktree.
	branch := "conductor/proj-1/T-1"
	gitT(t, repo, "checkout", "-q", "-b", branch)
	if err := os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatalf("write feature: %v", err)
	}
	gitT(t, repo, "add", ".")
	gitT(t, repo, "commit", "-q", "-m", "feature work")
	gitT(t, repo, "checkout", "-q", "develop")

	m := NewGitMerger(func(string) string { return repo })
	project := statestore.Project{ID: "proj-1", BaseBranch: "develop"}
	task := statestore.Task{ID: "T-1", ProjectID: "proj-1"}
	ws := engine.Workspace{Path: repo, Branch: branch}

	sha, err := m.SquashMerge(ctx, project, task, ws)
	if err != nil {
		t.Fatalf("SquashMerge: %v", err)
	}
	if sha == "" {
		t.Fatalf("empty merge sha")
	}
	// The base now has the feature content and the exact trailer.
	if _, err := os.Stat(filepath.Join(repo, "feature.txt")); err != nil {
		t.Fatalf("feature content not on base after squash-merge: %v", err)
	}
	body := gitT(t, repo, "log", "-1", "--format=%B", "develop")
	if !strings.Contains(body, "[task:T-1]") {
		t.Fatalf("merge commit missing [task:T-1] trailer:\n%s", body)
	}
	head := gitT(t, repo, "rev-parse", "develop")
	if head != sha {
		t.Fatalf("returned sha %q != develop HEAD %q", sha, head)
	}
}
