// C-2 regression tests at the conductor seam: the conductor's post-tick lease
// release is OWNER-SCOPED (ReleaseLeaseOwned with the project+host+task it actually
// acquired), never the owner-blind ReleaseLease. Before the fix, after a false-reap
// and re-acquire by ANOTHER host, the original holder's release deleted the NEW
// holder's lease → two hosts could drive the same repo (repo-per-1 / ADR-0008
// violation). These tests prove (1) the conductor calls ReleaseLeaseOwned with the
// exact owner, and (2) a different owner's lease for the same project SURVIVES the
// original holder's release. Hermetic: in-memory store, fake collaborators.
package conductor

import (
	"context"
	"testing"

	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/registry"
	"github.com/everva/conductor-platform/internal/statestore"
)

// spyPicker wraps the real registry Picker and records which release method the
// conductor invoked (and with what owner), so a test can prove the tick uses the
// owner-scoped release rather than the owner-blind one.
type spyPicker struct {
	inner            Picker
	releaseCalls     int // owner-blind ReleaseLease (must stay 0 for the conductor's own release)
	releaseOwnedArgs []releaseOwnedCall
}

type releaseOwnedCall struct {
	projectID, hostID, taskID string
}

func (p *spyPicker) PickReady(ctx context.Context, projectID string) (statestore.Task, error) {
	return p.inner.PickReady(ctx, projectID)
}

func (p *spyPicker) AcquireLease(ctx context.Context, l statestore.Lease) error {
	return p.inner.AcquireLease(ctx, l)
}

func (p *spyPicker) ReleaseLease(ctx context.Context, projectID string) error {
	p.releaseCalls++
	return p.inner.ReleaseLease(ctx, projectID)
}

func (p *spyPicker) ReleaseLeaseOwned(ctx context.Context, projectID, hostID, taskID string) error {
	p.releaseOwnedArgs = append(p.releaseOwnedArgs, releaseOwnedCall{projectID, hostID, taskID})
	return p.inner.ReleaseLeaseOwned(ctx, projectID, hostID, taskID)
}

// TestConductor_PostTickReleaseIsOwnerScoped proves a normal merging tick releases
// its lease via ReleaseLeaseOwned with the EXACT (project, host, task) it acquired,
// and never via the owner-blind ReleaseLease (C-2).
func TestConductor_PostTickReleaseIsOwnerScoped(t *testing.T) {
	ctx := context.Background()
	store := statestore.NewMemoryStore()
	if err := store.CreateProject(ctx, statestore.Project{ID: projectID, Repo: "owner/repo", BaseBranch: "develop"}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := store.CreateTask(ctx, statestore.Task{ID: "T-1", ProjectID: projectID, Lane: "x", Tier: "T2", Status: registry.StatusReady, Branch: "conductor/T-1"}); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	spy := &spyPicker{inner: registry.NewRegistry(store)}
	cond, err := New(Deps{
		Store:       store,
		Picker:      spy,
		Provisioner: &fakeProvisioner{},
		Engine:      &fakeEngine{verdict: engine.Verdict{Result: "ok", Branch: "conductor/T-1"}},
		Verifier:    &fakeVerifier{result: "pass"},
		Merger:      &fakeMerger{sha: "deadbeef"},
		HostID:      "host-A",
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

	if spy.releaseCalls != 0 {
		t.Fatalf("conductor used owner-BLIND ReleaseLease %d time(s); must use owner-scoped release (C-2)", spy.releaseCalls)
	}
	if len(spy.releaseOwnedArgs) != 1 {
		t.Fatalf("expected exactly 1 owner-scoped release, got %d (%+v)", len(spy.releaseOwnedArgs), spy.releaseOwnedArgs)
	}
	got := spy.releaseOwnedArgs[0]
	if got.projectID != projectID || got.hostID != "host-A" || got.taskID != "T-1" {
		t.Fatalf("owner-scoped release args = %+v, want {project=%s host=host-A task=T-1}", got, projectID)
	}

	// The lease the tick acquired is gone after its own owner-scoped release.
	if _, err := store.GetLease(ctx, projectID); err == nil {
		t.Fatal("tick's own lease should be released after the tick")
	}
}

// TestConductor_ReleaseDoesNotClobberNewOwner is the end-to-end C-2 scenario: while
// host-A's tick runs, the lease is FALSE-REAPED and host-B re-acquires the SAME
// project. When host-A's tick then releases, host-B's lease MUST survive — because
// the release is owner-scoped, A's release matches nothing and deletes nothing. We
// inject the reap+re-acquire via a provisioner hook that runs mid-tick (after the
// lease is acquired, before release), modelling the reconcile race precisely.
func TestConductor_ReleaseDoesNotClobberNewOwner(t *testing.T) {
	ctx := context.Background()
	store := statestore.NewMemoryStore()
	if err := store.CreateProject(ctx, statestore.Project{ID: projectID, Repo: "owner/repo", BaseBranch: "develop"}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := store.CreateTask(ctx, statestore.Task{ID: "T-1", ProjectID: projectID, Lane: "x", Tier: "T2", Status: registry.StatusReady, Branch: "conductor/T-1"}); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	// A provisioner whose Workspace hook fires mid-tick (after host-A acquired the
	// lease). There it simulates the reconcile reaper FALSE-reaping A's lease and
	// host-B re-acquiring the same project before A's tick completes.
	hookFired := false
	prov := &hookProvisioner{
		onWorkspace: func() {
			hookFired = true
			// Reaper force-releases A's lease (owner-blind, as the reaper legitimately does).
			if err := store.ReleaseLease(ctx, projectID); err != nil {
				t.Fatalf("simulated reap: %v", err)
			}
			// Host-B re-acquires the SAME project for its own task.
			if err := store.AcquireLease(ctx, statestore.Lease{ProjectID: projectID, HostID: "host-B", TaskID: "T-2"}); err != nil {
				t.Fatalf("host-B re-acquire: %v", err)
			}
		},
	}

	cond, err := New(Deps{
		Store:       store,
		Picker:      registry.NewRegistry(store),
		Provisioner: prov,
		Engine:      &fakeEngine{verdict: engine.Verdict{Result: "ok", Branch: "conductor/T-1"}},
		Verifier:    &fakeVerifier{result: "pass"},
		Merger:      &fakeMerger{sha: "deadbeef"},
		HostID:      "host-A",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := cond.Tick(ctx, projectID); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if !hookFired {
		t.Fatal("mid-tick reap+re-acquire hook never fired; test did not exercise the C-2 race")
	}

	// After host-A's tick released (owner-scoped), host-B's lease MUST still be held.
	l, err := store.GetLease(ctx, projectID)
	if err != nil {
		t.Fatalf("host-B's lease was clobbered by host-A's release (C-2 regression): %v", err)
	}
	if l.HostID != "host-B" || l.TaskID != "T-2" {
		t.Fatalf("lease owner = {host=%s task=%s}, want host-B/T-2 (C-2: new owner must survive)", l.HostID, l.TaskID)
	}
}

// hookProvisioner is a fakeProvisioner variant that runs a hook the first time
// Workspace is called (mid-tick, after the lease is held), so a test can inject a
// concurrent store mutation at a precise point. Cleanup is a no-op.
type hookProvisioner struct {
	onWorkspace func()
	called      bool
}

func (p *hookProvisioner) Workspace(_ context.Context, project statestore.Project, task statestore.Task) (engine.Workspace, error) {
	if !p.called && p.onWorkspace != nil {
		p.called = true
		p.onWorkspace()
	}
	return engine.Workspace{Branch: task.Branch, Path: "/tmp/ws/" + task.ID}, nil
}

func (p *hookProvisioner) Cleanup(_ context.Context, _ engine.Workspace) error { return nil }
