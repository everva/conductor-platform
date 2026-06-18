package conductor

import (
	"context"
	"errors"
	"testing"

	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/governance"
	"github.com/everva/conductor-platform/internal/registry"
	"github.com/everva/conductor-platform/internal/statestore"
	"github.com/everva/conductor-platform/internal/verify"
)

// reattachProvisioner is a fakeProvisioner that ALSO satisfies ReAttacher, recording
// WorkspaceForBranch calls (the approve-merge re-attach path) separately from
// Workspace calls (the develop path). It does no real git; the real git mechanics
// (branch survival + re-attach) are proven by the e2e test. Here we prove the
// ORCHESTRATION: that approve re-attaches the preserved branch and never re-develops.
type reattachProvisioner struct {
	wsCalls       int
	reattachCalls int
	reattachBr    string
	cleanupCalls  int
}

func (f *reattachProvisioner) Workspace(_ context.Context, _ statestore.Project, task statestore.Task) (engine.Workspace, error) {
	f.wsCalls++
	return engine.Workspace{Path: "/tmp/ws/" + task.ID, Branch: "conductor/" + task.ID}, nil
}

func (f *reattachProvisioner) WorkspaceForBranch(_ context.Context, _ statestore.Project, task statestore.Task, branch string) (engine.Workspace, error) {
	f.reattachCalls++
	f.reattachBr = branch
	return engine.Workspace{Path: "/tmp/ws/" + task.ID, Branch: branch}, nil
}

func (f *reattachProvisioner) Cleanup(_ context.Context, _ engine.Workspace) error {
	f.cleanupCalls++
	return nil
}

// TestConductor_HoldApproveMerge_DevelopRunsOnce is the DETERMINISTIC proof of the
// whole human-hold approve flow (Faz-1.5-b), with no real daemon and no real git:
//
//	Tick 1: a T4 task whose gate PASSES is HELD (awaiting-approval), NOT merged, and
//	        its verified branch is recorded — develop ran exactly once.
//	approve: an operator marks it approved through the SHARED store (StoreApprover).
//	Tick 2: the approved held task is MERGED by re-attaching the PRESERVED branch and
//	        re-verifying — develop is STILL at one (it never re-rolled), the merge
//	        used the preserved branch, and the task ends done.
func TestConductor_HoldApproveMerge_DevelopRunsOnce(t *testing.T) {
	ctx := context.Background()
	store := statestore.NewMemoryStore()
	if err := store.CreateProject(ctx, statestore.Project{ID: projectID, Repo: "owner/repo", BaseBranch: "develop"}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := store.CreateTask(ctx, statestore.Task{ID: "T-1", ProjectID: projectID, Lane: "x", Tier: governance.TierT4, Status: registry.StatusReady}); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	eng := &fakeEngine{verdict: engine.Verdict{Result: "pass"}}
	prov := &reattachProvisioner{}
	verf := &fakeVerifier{result: "pass"}
	merge := &fakeMerger{sha: "approvedsha"}

	cond, err := New(Deps{
		Store:       store,
		Picker:      registry.NewRegistry(store),
		Provisioner: prov,
		Engine:      eng,
		Verifier:    verf,
		Merger:      merge,
		HostID:      "host-1",
		Policy:      governance.DefaultPolicy(), // T4 -> human-required
		Approver:    NewStoreApprover(store),    // the approve-merge resolver
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// --- Tick 1: develop -> verify pass -> HELD (no merge) --------------------
	res1, err := cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("tick 1: %v", err)
	}
	if res1.Outcome != OutcomeHeld {
		t.Fatalf("tick 1 outcome = %q, want held", res1.Outcome)
	}
	if merge.calls != 0 {
		t.Fatalf("tick 1 must NOT merge a held task, got %d merge calls", merge.calls)
	}
	if eng.developed != 1 {
		t.Fatalf("tick 1: develop ran %d times, want 1", eng.developed)
	}
	held, _ := store.GetTask(ctx, "T-1")
	if held.Status != StatusAwaitingApproval {
		t.Fatalf("held status = %q, want %q", held.Status, StatusAwaitingApproval)
	}
	if held.Branch == "" {
		t.Fatalf("held task must record the verified branch")
	}
	if held.Approved {
		t.Fatalf("held task must not be pre-approved")
	}

	// --- approve via the SHARED store (mirrors `conductorctl approve`) ---------
	approvedID, err := NewStoreApprover(store).RequestApprove(ctx, projectID, "")
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if approvedID != "T-1" {
		t.Fatalf("approved id = %q, want T-1", approvedID)
	}

	// --- Tick 2: approved -> re-attach preserved branch -> re-verify -> merge --
	res2, err := cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("tick 2: %v", err)
	}
	if res2.Outcome != OutcomeApprovedMerged {
		t.Fatalf("tick 2 outcome = %q, want approved-merged; res=%+v", res2.Outcome, res2)
	}
	if res2.MergeSHA != "approvedsha" {
		t.Fatalf("tick 2 merge sha = %q, want approvedsha", res2.MergeSHA)
	}

	// THE PROOF: develop ran exactly ONCE across both ticks (no re-develop on approve).
	if eng.developed != 1 {
		t.Fatalf("develop ran %d times total, want 1 (approve must NOT re-develop)", eng.developed)
	}
	// The merge re-attached the PRESERVED verified branch, not a fresh cut.
	if prov.reattachCalls != 1 {
		t.Fatalf("re-attach (WorkspaceForBranch) calls = %d, want 1", prov.reattachCalls)
	}
	if prov.reattachBr != held.Branch {
		t.Fatalf("re-attached branch = %q, want the preserved %q", prov.reattachBr, held.Branch)
	}
	// Workspace (develop cut) ran only on tick 1, never on the approve tick.
	if prov.wsCalls != 1 {
		t.Fatalf("Workspace (develop cut) calls = %d, want 1 (approve must not cut a fresh worktree)", prov.wsCalls)
	}
	if merge.calls != 1 {
		t.Fatalf("merge calls = %d, want 1 (the approve-merge)", merge.calls)
	}
	// verify ran twice: once at hold (the gate), once on approve (the cheap re-verify).
	if verf.calls != 2 {
		t.Fatalf("verify calls = %d, want 2 (gate + re-verify)", verf.calls)
	}
	done, _ := store.GetTask(ctx, "T-1")
	if done.Status != registry.StatusDone {
		t.Fatalf("final status = %q, want done", done.Status)
	}
	if done.Approved {
		t.Fatalf("merged task must clear its approval flag, got Approved=true")
	}
	_ = verify.Gate{} // keep verify import meaningful for the seam types used above
}

// TestConductor_ApproveReVerifyFailsBaseDrift proves the honest no-fake-green path:
// if the preserved branch fails the cheap re-verify on approval (base drifted), the
// conductor does NOT merge and blocks the task, clearing the approval — and STILL
// never re-develops.
func TestConductor_ApproveReVerifyFailsBaseDrift(t *testing.T) {
	ctx := context.Background()
	store := statestore.NewMemoryStore()
	if err := store.CreateProject(ctx, statestore.Project{ID: projectID, Repo: "owner/repo", BaseBranch: "develop"}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	// Seed the task ALREADY held + approved + branch recorded (post-hold, post-approve
	// state), so this tick exercises only the approve-merge re-verify-fail path.
	if err := store.CreateTask(ctx, statestore.Task{
		ID: "T-1", ProjectID: projectID, Lane: "x", Tier: governance.TierT4,
		Status: StatusAwaitingApproval, Branch: "conductor/T-1", Approved: true,
	}); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	eng := &fakeEngine{verdict: engine.Verdict{Result: "pass"}}
	prov := &reattachProvisioner{}
	verf := &fakeVerifier{result: "changes-requested"} // re-verify FAILS (base drift)
	merge := &fakeMerger{sha: "shouldnothappen"}

	cond, err := New(Deps{
		Store: store, Picker: registry.NewRegistry(store), Provisioner: prov,
		Engine: eng, Verifier: verf, Merger: merge, HostID: "host-1",
		Policy: governance.DefaultPolicy(), Approver: NewStoreApprover(store),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if res.Outcome != OutcomeApprovedRejected {
		t.Fatalf("outcome = %q, want approved-rejected", res.Outcome)
	}
	if merge.calls != 0 {
		t.Fatalf("re-verify-fail must NOT merge (Rule#9), got %d merge calls", merge.calls)
	}
	if eng.developed != 0 {
		t.Fatalf("approve-merge must NEVER develop, got %d", eng.developed)
	}
	got, _ := store.GetTask(ctx, "T-1")
	if got.Status != registry.StatusBlocked {
		t.Fatalf("rejected task status = %q, want blocked", got.Status)
	}
	if got.Approved {
		t.Fatalf("rejected task must clear approval so a re-approval is a fresh decision")
	}
}

// TestStoreApprover_Resolution covers the operator-side approve resolution edges:
// nothing-held, ambiguity, and explicit-task validation, mirroring abort's
// resolution tests.
func TestStoreApprover_Resolution(t *testing.T) {
	ctx := context.Background()
	store := statestore.NewMemoryStore()
	if err := store.CreateProject(ctx, statestore.Project{ID: projectID, BaseBranch: "develop"}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	ap := NewStoreApprover(store)

	// Nothing held -> ErrNothingToApprove.
	if _, err := ap.RequestApprove(ctx, projectID, ""); !errors.Is(err, ErrNothingToApprove) {
		t.Fatalf("empty project: err = %v, want ErrNothingToApprove", err)
	}

	// A ready (not-held) task is not approvable by explicit id.
	if err := store.CreateTask(ctx, statestore.Task{ID: "R-1", ProjectID: projectID, Status: registry.StatusReady}); err != nil {
		t.Fatalf("seed ready task: %v", err)
	}
	if _, err := ap.RequestApprove(ctx, projectID, "R-1"); !errors.Is(err, ErrNothingToApprove) {
		t.Fatalf("ready task: err = %v, want ErrNothingToApprove", err)
	}

	// Two held tasks -> ambiguity without --task.
	for _, id := range []string{"H-1", "H-2"} {
		if err := store.CreateTask(ctx, statestore.Task{ID: id, ProjectID: projectID, Status: StatusAwaitingApproval, Branch: "conductor/" + id}); err != nil {
			t.Fatalf("seed held %s: %v", id, err)
		}
	}
	if _, err := ap.RequestApprove(ctx, projectID, ""); !errors.Is(err, ErrAmbiguousApproval) {
		t.Fatalf("two held: err = %v, want ErrAmbiguousApproval", err)
	}

	// Explicit --task disambiguates and flips the flag.
	id, err := ap.RequestApprove(ctx, projectID, "H-2")
	if err != nil {
		t.Fatalf("explicit approve: %v", err)
	}
	if id != "H-2" {
		t.Fatalf("approved id = %q, want H-2", id)
	}
	got, _ := store.GetTask(ctx, "H-2")
	if !got.Approved {
		t.Fatalf("H-2 must be marked approved")
	}
	// PendingApproval surfaces exactly the approved-and-held task.
	pend, ok, err := ap.PendingApproval(ctx, projectID)
	if err != nil || !ok || pend.ID != "H-2" {
		t.Fatalf("PendingApproval = (%+v, %v, %v), want H-2", pend, ok, err)
	}
}
