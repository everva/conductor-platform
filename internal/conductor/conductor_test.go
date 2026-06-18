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
	"github.com/everva/conductor-platform/internal/events"
	"github.com/everva/conductor-platform/internal/governance"
	"github.com/everva/conductor-platform/internal/governor"
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

// fakeGovernor is a scripted Admitter: it returns a fixed Decision so the tick's
// admission gate can be driven without a real lease table or load probe.
type fakeGovernor struct {
	dec   governor.Decision
	err   error
	calls int
}

func (f *fakeGovernor) Admit(context.Context, string) (governor.Decision, error) {
	f.calls++
	return f.dec, f.err
}

// TestConductor_Tick_GovernorDeny_NoOps proves a denied admission makes the tick
// a clean no-op: no lease, no workspace, no develop, no merge — and the task
// lifecycle is untouched (still ready), with the structured deny reason surfaced.
func TestConductor_Tick_GovernorDeny_NoOps(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, engine.Verdict{Result: "pass"}, nil, "pass", nil)

	gov := &fakeGovernor{dec: governor.Decision{Admit: false, Reason: governor.ReasonDenyGlobalCap}}
	cond, err := New(Deps{
		Store:       h.store,
		Picker:      registry.NewRegistry(h.store),
		Provisioner: h.prov,
		Engine:      h.eng,
		Verifier:    h.verf,
		Merger:      h.merge,
		HostID:      "host-1",
		Governor:    gov,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if res.Outcome != OutcomeDenied {
		t.Fatalf("outcome = %q, want denied", res.Outcome)
	}
	if res.DenyReason != governor.ReasonDenyGlobalCap {
		t.Fatalf("deny reason = %q, want %q", res.DenyReason, governor.ReasonDenyGlobalCap)
	}
	if gov.calls != 1 {
		t.Fatalf("governor consulted %d times, want 1", gov.calls)
	}
	// The denied tick touched nothing: no lease, no work, task unchanged.
	if h.leaseHeld(t) {
		t.Fatalf("denied tick must not take a lease")
	}
	if h.prov.wsCalls != 0 || h.eng.developed != 0 || h.merge.calls != 0 {
		t.Fatalf("denied tick must do no work: ws=%d dev=%d merge=%d", h.prov.wsCalls, h.eng.developed, h.merge.calls)
	}
	if got := h.task(t).Status; got != registry.StatusReady {
		t.Fatalf("task status = %q, want unchanged ready", got)
	}
}

// TestConductor_Tick_GovernorAdmit_Proceeds proves an admit lets the green path
// run exactly as without a governor (the gate is non-intrusive when it admits).
func TestConductor_Tick_GovernorAdmit_Proceeds(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, engine.Verdict{Result: "pass"}, nil, "pass", nil)

	gov := &fakeGovernor{dec: governor.Decision{Admit: true, Reason: governor.ReasonAdmit}}
	cond, err := New(Deps{
		Store:       h.store,
		Picker:      registry.NewRegistry(h.store),
		Provisioner: h.prov,
		Engine:      h.eng,
		Verifier:    h.verf,
		Merger:      h.merge,
		HostID:      "host-1",
		Governor:    gov,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if res.Outcome != OutcomeMerged {
		t.Fatalf("outcome = %q, want merged", res.Outcome)
	}
	if gov.calls != 1 {
		t.Fatalf("governor consulted %d times, want 1", gov.calls)
	}
	if h.merge.calls != 1 {
		t.Fatalf("admit must let the merge happen, got %d", h.merge.calls)
	}
}

// TestConductor_Tick_HumanRequiredTier_HeldNotMerged proves a high-tier task whose
// independent gate PASSES is NOT auto-merged when a human-required governance policy
// is injected: no merge call, the task is parked in awaiting-approval (NOT blocked)
// with its VERIFIED branch preserved on Task.Branch, the held outcome + structured
// hold reason are surfaced, and an intervention-needed event is emitted. The base
// never gets a [task:<id>] trailer for a held task.
func TestConductor_Tick_HumanRequiredTier_HeldNotMerged(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, engine.Verdict{Result: "pass"}, nil, "pass", nil)
	// Make the only task a high-risk tier the default policy holds for a human.
	tk := h.task(t)
	tk.Tier = governance.TierT4
	if err := h.store.UpdateTask(ctx, tk); err != nil {
		t.Fatalf("set tier: %v", err)
	}

	em := &recordingEmitter{}
	cond, err := New(Deps{
		Store:       h.store,
		Picker:      registry.NewRegistry(h.store),
		Provisioner: h.prov,
		Engine:      h.eng,
		Verifier:    h.verf,
		Merger:      h.merge,
		HostID:      "host-1",
		Emitter:     em,
		Policy:      governance.DefaultPolicy(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if res.Outcome != OutcomeHeld {
		t.Fatalf("outcome = %q, want held; res=%+v", res.Outcome, res)
	}
	if res.HoldReason != governance.ReasonHighTier {
		t.Fatalf("hold reason = %q, want %q", res.HoldReason, governance.ReasonHighTier)
	}
	if h.merge.calls != 0 {
		t.Fatalf("human-required tier must NOT merge, got %d merge calls", h.merge.calls)
	}
	// Verify still ran (the gate is necessary); only the merge was withheld.
	if h.verf.calls != 1 {
		t.Fatalf("verify calls = %d, want 1 (gate runs before the hold)", h.verf.calls)
	}
	heldTask := h.task(t)
	if heldTask.Status != StatusAwaitingApproval {
		t.Fatalf("held task status = %q, want %q (awaiting human, not blocked)", heldTask.Status, StatusAwaitingApproval)
	}
	if heldTask.Branch == "" {
		t.Fatalf("held task must record its verified branch for the later approve-merge; got empty")
	}
	if !em.hasKind(events.KindInterventionNeeded) {
		t.Fatalf("held task must emit intervention-needed; got kinds %v", em.kinds())
	}
	if h.leaseHeld(t) {
		t.Fatalf("lease must be released after a held tick")
	}
	if h.prov.cleanupCalls != 1 {
		t.Fatalf("worktree cleanup calls = %d, want 1", h.prov.cleanupCalls)
	}
}

// TestConductor_Tick_AutoMergeTier_StillMerges proves the policy is non-intrusive
// for low-risk tiers: a T2 task whose gate passes auto-merges exactly as without a
// policy, and emits no intervention-needed event.
func TestConductor_Tick_AutoMergeTier_StillMerges(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, engine.Verdict{Result: "pass"}, nil, "pass", nil) // seeded tier is T2

	em := &recordingEmitter{}
	cond, err := New(Deps{
		Store:       h.store,
		Picker:      registry.NewRegistry(h.store),
		Provisioner: h.prov,
		Engine:      h.eng,
		Verifier:    h.verf,
		Merger:      h.merge,
		HostID:      "host-1",
		Emitter:     em,
		Policy:      governance.DefaultPolicy(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if res.Outcome != OutcomeMerged {
		t.Fatalf("outcome = %q, want merged (auto-merge tier)", res.Outcome)
	}
	if h.merge.calls != 1 {
		t.Fatalf("auto-merge tier must merge once, got %d", h.merge.calls)
	}
	if got := h.task(t).Status; got != registry.StatusDone {
		t.Fatalf("task status = %q, want done", got)
	}
	if em.hasKind(events.KindInterventionNeeded) {
		t.Fatalf("auto-merge tier must NOT emit intervention-needed; kinds %v", em.kinds())
	}
}

// TestConductor_Tick_NilPolicy_AutoMergesHighTier proves the seam is backward
// compatible: with NO policy injected, even a T4 task auto-merges (pre-N-10
// behavior), which is exactly why existing tests stay green.
func TestConductor_Tick_NilPolicy_AutoMergesHighTier(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, engine.Verdict{Result: "pass"}, nil, "pass", nil)
	tk := h.task(t)
	tk.Tier = governance.TierT4
	if err := h.store.UpdateTask(ctx, tk); err != nil {
		t.Fatalf("set tier: %v", err)
	}

	res, err := h.cond.Tick(ctx, projectID) // harness conductor has no Policy
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if res.Outcome != OutcomeMerged {
		t.Fatalf("outcome = %q, want merged (nil policy = auto-merge-all)", res.Outcome)
	}
	if h.merge.calls != 1 {
		t.Fatalf("nil policy must auto-merge even T4, got %d merge calls", h.merge.calls)
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

// TestGitMerger_SquashMerge_ConflictRestoresCleanBase proves FIX #4: when the
// per-task branch CONFLICTS with the base (both modify the same line divergently),
// SquashMerge returns an error AND leaves the base checkout pristine — the base
// branch is back at its original commit, `git status --porcelain` is EMPTY, and no
// merge state is left behind. A dirty/conflicted base would corrupt the next tick.
func TestGitMerger_SquashMerge_ConflictRestoresCleanBase(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "develop")
	gitT(t, repo, "config", "user.name", "test")
	gitT(t, repo, "config", "user.email", "test@test")

	conflictFile := filepath.Join(repo, "conflict.txt")
	if err := os.WriteFile(conflictFile, []byte("original\n"), 0o644); err != nil {
		t.Fatalf("write base: %v", err)
	}
	gitT(t, repo, "add", ".")
	gitT(t, repo, "commit", "-q", "-m", "base")

	// Cut a task branch that changes the SAME line one way...
	branch := "conductor/proj-1/T-conflict"
	gitT(t, repo, "checkout", "-q", "-b", branch)
	if err := os.WriteFile(conflictFile, []byte("from-branch\n"), 0o644); err != nil {
		t.Fatalf("write branch: %v", err)
	}
	gitT(t, repo, "add", ".")
	gitT(t, repo, "commit", "-q", "-m", "branch change")

	// ...and advance the base to change the SAME line the OTHER way, so a squash
	// merge of the branch into the base genuinely conflicts.
	gitT(t, repo, "checkout", "-q", "develop")
	if err := os.WriteFile(conflictFile, []byte("from-base\n"), 0o644); err != nil {
		t.Fatalf("write base divergent: %v", err)
	}
	gitT(t, repo, "add", ".")
	gitT(t, repo, "commit", "-q", "-m", "base diverges")

	baseBefore := gitT(t, repo, "rev-parse", "develop")

	m := NewGitMerger(func(string) string { return repo })
	project := statestore.Project{ID: "proj-1", BaseBranch: "develop"}
	task := statestore.Task{ID: "T-conflict", ProjectID: "proj-1"}
	ws := engine.Workspace{Path: repo, Branch: branch}

	sha, err := m.SquashMerge(ctx, project, task, ws)
	if err == nil {
		t.Fatalf("SquashMerge: expected conflict error, got sha %q", sha)
	}
	if sha != "" {
		t.Fatalf("SquashMerge: expected empty sha on failure, got %q", sha)
	}

	// The base ref must NOT have advanced.
	baseAfter := gitT(t, repo, "rev-parse", "develop")
	if baseAfter != baseBefore {
		t.Fatalf("base ref advanced on failed merge: before=%s after=%s", baseBefore, baseAfter)
	}

	// The base checkout must be CLEAN: no conflict markers, no staged residue, no
	// untracked files, no in-progress merge state.
	if status := gitT(t, repo, "status", "--porcelain"); status != "" {
		t.Fatalf("base checkout dirty after failed merge:\n%s", status)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "MERGE_HEAD")); !os.IsNotExist(err) {
		t.Fatalf("MERGE_HEAD still present after failed merge (err=%v)", err)
	}

	// The conflicting file must hold the BASE content, not a merged/marked version.
	got, err := os.ReadFile(conflictFile)
	if err != nil {
		t.Fatalf("read conflict file: %v", err)
	}
	if string(got) != "from-base\n" {
		t.Fatalf("conflict file not restored to base content, got %q", got)
	}
}
