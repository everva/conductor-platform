package reconcile

import (
	"context"
	"testing"
	"time"

	"github.com/everva/conductor-platform/internal/statestore"
)

const proj = "proj-1"

func newStore(t *testing.T) statestore.StateStore {
	t.Helper()
	ctx := context.Background()
	s := statestore.NewMemoryStore()
	if err := s.CreateProject(ctx, statestore.Project{ID: proj, Repo: "owner/repo", BaseBranch: "develop"}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return s
}

// fakeGitLog is an injected git reader: it returns a fixed list of base-branch
// commits so tests need no real repo (decoupling, ADR-0016).
type fakeGitLog struct {
	commits []Commit
}

func (f fakeGitLog) BaseCommits(_ context.Context, _ statestore.Project) ([]Commit, error) {
	return f.commits, nil
}

func TestReconciler_ReapLeases_TTLExpired(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)

	// Stale lease (2h old) and a fresh lease (1m old).
	if err := s.CreateProject(ctx, statestore.Project{ID: "proj-2", Repo: "o/r", BaseBranch: "develop"}); err != nil {
		t.Fatalf("seed proj-2: %v", err)
	}
	mustAcquire(t, s, statestore.Lease{ProjectID: proj, HostID: "h1", TaskID: "T-1", AcquiredAt: now.Add(-2 * time.Hour)})
	mustAcquire(t, s, statestore.Lease{ProjectID: "proj-2", HostID: "h1", TaskID: "T-9", AcquiredAt: now.Add(-1 * time.Minute)})

	r := New(s, fakeGitLog{}, Config{LeaseTTL: time.Hour})
	if err := r.ReapLeases(ctx, now); err != nil {
		t.Fatalf("ReapLeases: %v", err)
	}

	if _, err := s.GetLease(ctx, proj); err == nil {
		t.Fatalf("stale lease should have been reaped")
	}
	if _, err := s.GetLease(ctx, "proj-2"); err != nil {
		t.Fatalf("fresh lease must be left untouched, got %v", err)
	}
}

func TestReconciler_ReapLeases_DeadOwner(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)

	// Fresh lease (TTL not expired) but its owner is dead.
	mustAcquire(t, s, statestore.Lease{ProjectID: proj, HostID: "dead-host", TaskID: "T-1", AcquiredAt: now.Add(-1 * time.Minute)})

	r := New(s, fakeGitLog{}, Config{
		LeaseTTL:  time.Hour,
		OwnerLive: func(l statestore.Lease) bool { return l.HostID != "dead-host" },
	})
	if err := r.ReapLeases(ctx, now); err != nil {
		t.Fatalf("ReapLeases: %v", err)
	}
	if _, err := s.GetLease(ctx, proj); err == nil {
		t.Fatalf("dead-owner lease should have been reaped regardless of age")
	}
}

func TestReconciler_ReconcileTasks_TrailerMarksDone(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedTask(t, s, statestore.Task{ID: "T-1", ProjectID: proj, Status: "running"})

	gl := fakeGitLog{commits: []Commit{
		{SHA: "abc", Subject: "feat: thing", Trailers: []string{"[task:T-1]"}},
	}}
	r := New(s, gl, Config{LeaseTTL: time.Hour})

	p, _ := s.GetProject(ctx, proj)
	if err := r.ReconcileTasks(ctx, p); err != nil {
		t.Fatalf("ReconcileTasks: %v", err)
	}
	got, _ := s.GetTask(ctx, "T-1")
	if got.Status != "done" {
		t.Fatalf("trailer commit should mark task done, got %q", got.Status)
	}
}

func TestReconciler_NoTrailer_NoChange(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedTask(t, s, statestore.Task{ID: "T-1", ProjectID: proj, Status: "running"})

	// Commit with no trailer; and a malformed/unknown-task trailer (exact match
	// only, no fuzzy/substring matching).
	gl := fakeGitLog{commits: []Commit{
		{SHA: "abc", Subject: "feat: no trailer here"},
		{SHA: "def", Subject: "feat: mentions T-1 in body but trailer is wrong", Trailers: []string{"[task:T-1-extra]"}},
		{SHA: "ghi", Subject: "feat: unknown", Trailers: []string{"[task:T-999]"}},
	}}
	r := New(s, gl, Config{LeaseTTL: time.Hour})

	p, _ := s.GetProject(ctx, proj)
	if err := r.ReconcileTasks(ctx, p); err != nil {
		t.Fatalf("ReconcileTasks: %v", err)
	}
	got, _ := s.GetTask(ctx, "T-1")
	if got.Status != "running" {
		t.Fatalf("no exact trailer -> task must not flip, got %q", got.Status)
	}
}

func TestReconciler_Idempotent(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	seedTask(t, s, statestore.Task{ID: "T-1", ProjectID: proj, Status: "running"})
	mustAcquire(t, s, statestore.Lease{ProjectID: proj, HostID: "h1", TaskID: "T-1", AcquiredAt: now.Add(-2 * time.Hour)})

	gl := fakeGitLog{commits: []Commit{{SHA: "abc", Subject: "x", Trailers: []string{"[task:T-1]"}}}}

	// Count store writes via a wrapper.
	cw := &countingStore{StateStore: s}
	r := New(cw, gl, Config{LeaseTTL: time.Hour})
	p, _ := s.GetProject(ctx, proj)

	run := func() {
		if err := r.ReapLeases(ctx, now); err != nil {
			t.Fatalf("reap: %v", err)
		}
		if err := r.ReconcileTasks(ctx, p); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
	}
	run()
	writesAfterFirst := cw.updates + cw.releases
	run()
	writesAfterSecond := cw.updates + cw.releases

	if writesAfterSecond != writesAfterFirst {
		t.Fatalf("not idempotent: extra writes on second pass (first=%d second=%d)", writesAfterFirst, writesAfterSecond)
	}
	got, _ := s.GetTask(ctx, "T-1")
	if got.Status != "done" {
		t.Fatalf("task should be done and stay done, got %q", got.Status)
	}
}

// TestHostHeartbeatOwnerLive_StaleHostReaped proves the cross-host stale-lease
// reaping path (2B-3): a host whose registry heartbeat is stale is treated as DEAD
// so its lease is reaped, while a host with a fresh heartbeat is LIVE so its lease
// is kept — driven entirely by the host registry's LastHeartbeat (not PID), the
// only liveness signal meaningful across machines.
func TestHostHeartbeatOwnerLive_StaleHostReaped(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	const hostStale = 90 * time.Second

	// host-mac: stale heartbeat (5m ago) holding the lease on proj.
	// host-linux: fresh heartbeat (10s ago) holding a lease on proj-2.
	if err := s.CreateProject(ctx, statestore.Project{ID: "proj-2", Repo: "o/r", BaseBranch: "develop"}); err != nil {
		t.Fatalf("seed proj-2: %v", err)
	}
	if err := s.RegisterHost(ctx, statestore.Host{ID: "host-mac", Capabilities: []string{"ios-build"}, LastHeartbeat: now.Add(-5 * time.Minute)}); err != nil {
		t.Fatalf("register host-mac: %v", err)
	}
	if err := s.RegisterHost(ctx, statestore.Host{ID: "host-linux", Capabilities: []string{"linux"}, LastHeartbeat: now.Add(-10 * time.Second)}); err != nil {
		t.Fatalf("register host-linux: %v", err)
	}
	mustAcquire(t, s, statestore.Lease{ProjectID: proj, HostID: "host-mac", TaskID: "T-ios", AcquiredAt: now.Add(-1 * time.Minute)})
	mustAcquire(t, s, statestore.Lease{ProjectID: "proj-2", HostID: "host-linux", TaskID: "T-generic", AcquiredAt: now.Add(-1 * time.Minute)})

	// LeaseTTL deliberately LONG so TTL does NOT reap; only the host-heartbeat
	// OwnerLive should free the stale host's lease (isolates the new predicate).
	ol := HostHeartbeatOwnerLive(ctx, s, hostStale, now)
	r := New(s, fakeGitLog{}, Config{LeaseTTL: time.Hour, OwnerLive: ol})
	if err := r.ReapLeases(ctx, now); err != nil {
		t.Fatalf("ReapLeases: %v", err)
	}

	if _, err := s.GetLease(ctx, proj); err == nil {
		t.Fatalf("stale-host (host-mac) lease should have been reaped")
	}
	if _, err := s.GetLease(ctx, "proj-2"); err != nil {
		t.Fatalf("fresh-host (host-linux) lease must be kept, got %v", err)
	}
}

// TestHostHeartbeatOwnerLive_UnknownHostReaped proves the conservative
// missing-host rule: a lease whose owner has NO registry row is treated as
// dead/unknown and reaped (it cannot prove liveness).
func TestHostHeartbeatOwnerLive_UnknownHostReaped(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

	mustAcquire(t, s, statestore.Lease{ProjectID: proj, HostID: "ghost", TaskID: "T-1", AcquiredAt: now.Add(-1 * time.Second)})

	ol := HostHeartbeatOwnerLive(ctx, s, time.Minute, now)
	r := New(s, fakeGitLog{}, Config{LeaseTTL: time.Hour, OwnerLive: ol})
	if err := r.ReapLeases(ctx, now); err != nil {
		t.Fatalf("ReapLeases: %v", err)
	}
	if _, err := s.GetLease(ctx, proj); err == nil {
		t.Fatalf("unregistered-host lease should have been reaped (conservative)")
	}
}

// TestHostHeartbeatOwnerLive_FreshHostKept asserts the predicate alone (no TTL)
// keeps a fresh host's lease: OwnerLive returns true so isStale is false.
func TestHostHeartbeatOwnerLive_FreshHostKept(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

	if err := s.RegisterHost(ctx, statestore.Host{ID: "live", LastHeartbeat: now.Add(-1 * time.Second)}); err != nil {
		t.Fatalf("register: %v", err)
	}
	mustAcquire(t, s, statestore.Lease{ProjectID: proj, HostID: "live", TaskID: "T-1", AcquiredAt: now.Add(-2 * time.Hour)})

	// LeaseTTL=0 disables the TTL backstop, so ONLY OwnerLive can decide; a fresh
	// host must keep its lease even though it is 2h old.
	ol := HostHeartbeatOwnerLive(ctx, s, time.Minute, now)
	r := New(s, fakeGitLog{}, Config{LeaseTTL: 0, OwnerLive: ol})
	if err := r.ReapLeases(ctx, now); err != nil {
		t.Fatalf("ReapLeases: %v", err)
	}
	if _, err := s.GetLease(ctx, proj); err != nil {
		t.Fatalf("fresh host's lease must NOT be reaped, got %v", err)
	}
}

// --- helpers ---

func mustAcquire(t *testing.T, s statestore.StateStore, l statestore.Lease) {
	t.Helper()
	if err := s.AcquireLease(context.Background(), l); err != nil {
		t.Fatalf("acquire lease %s: %v", l.ProjectID, err)
	}
}

func seedTask(t *testing.T, s statestore.StateStore, tk statestore.Task) {
	t.Helper()
	if err := s.CreateTask(context.Background(), tk); err != nil {
		t.Fatalf("seed task %s: %v", tk.ID, err)
	}
}

// countingStore wraps a StateStore to count the mutating verbs the reconciler
// uses, so idempotency (no duplicate writes) can be asserted.
type countingStore struct {
	statestore.StateStore
	updates  int
	releases int
}

func (c *countingStore) UpdateTask(ctx context.Context, t statestore.Task) error {
	c.updates++
	return c.StateStore.UpdateTask(ctx, t)
}

func (c *countingStore) ReleaseLease(ctx context.Context, projectID string) error {
	c.releases++
	return c.StateStore.ReleaseLease(ctx, projectID)
}
