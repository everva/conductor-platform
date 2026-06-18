package conductor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/everva/conductor-platform/internal/reconcile"
	"github.com/everva/conductor-platform/internal/registry"
	"github.com/everva/conductor-platform/internal/statestore"
)

// TestTwoHost_Coordination is the deterministic, network-free PROOF that the
// agent-per-host model (ADR-0024, 2B-3) coordinates two daemons correctly over ONE
// shared StateStore. It represents two hosts — host-linux [linux,backend] and
// host-mac [ios-build,macos] — as two registry.Registry instances (each built
// WithCapabilities = that host's caps, exactly as cmd/conductor wires the Picker)
// sharing the SAME store, against one project with two tasks: T-ios (Requires
// [ios-build]) and T-generic (no Requires). It asserts the three coordination
// guarantees of the model:
//
//  1. Host-spanning single-winner lease (N-4): both agents race AcquireLease on the
//     SAME repo → exactly ONE wins, the other gets ErrLeaseHeld (no double-write).
//  2. Capability routing (2B-2): T-ios is pickable ONLY by host-mac; host-linux
//     skips it (it picks T-generic instead).
//  3. Cross-host reap (2B-3): host-mac goes dead (stale heartbeat) while holding a
//     lease → the host-heartbeat OwnerLive frees it; a fresh host's lease is kept;
//     after the reap a capable host can re-acquire the repo.
//
// No real network, no PID-liveness, no wall clock: every time is injected and the
// store is in-memory, so the proof is gate-safe and reproducible.
func TestTwoHost_Coordination(t *testing.T) {
	ctx := context.Background()
	const projectID = "proj-two-host"
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

	store := statestore.NewMemoryStore()

	// One shared project (the repo both agents contend for).
	if err := store.CreateProject(ctx, statestore.Project{ID: projectID, Repo: "owner/repo", BaseBranch: "develop"}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	// Two tasks: T-ios needs ios-build (mac only); T-generic needs nothing (any host).
	if err := store.CreateTask(ctx, statestore.Task{ID: "T-generic", ProjectID: projectID, Status: registry.StatusReady}); err != nil {
		t.Fatalf("seed T-generic: %v", err)
	}
	if err := store.CreateTask(ctx, statestore.Task{ID: "T-ios", ProjectID: projectID, Status: registry.StatusReady, Requires: []string{"ios-build"}}); err != nil {
		t.Fatalf("seed T-ios: %v", err)
	}

	// Two agents, each a Registry over the SHARED store with its host's capabilities
	// (the same construction cmd/conductor uses for the Picker).
	const (
		hostLinux = "host-linux"
		hostMac   = "host-mac"
	)
	agentLinux := registry.NewRegistry(store, registry.WithCapabilities([]string{"linux", "backend"}))
	agentMac := registry.NewRegistry(store, registry.WithCapabilities([]string{"ios-build", "macos"}))

	// Self-register both hosts (fresh heartbeats) as the daemons would on startup.
	if err := store.RegisterHost(ctx, statestore.Host{ID: hostLinux, Capabilities: []string{"linux", "backend"}, LastHeartbeat: now}); err != nil {
		t.Fatalf("register host-linux: %v", err)
	}
	if err := store.RegisterHost(ctx, statestore.Host{ID: hostMac, Capabilities: []string{"ios-build", "macos"}, LastHeartbeat: now}); err != nil {
		t.Fatalf("register host-mac: %v", err)
	}

	// -------------------------------------------------------------------------
	// (2) Capability routing: T-ios is pickable ONLY by host-mac; host-linux skips
	// it and picks T-generic. Asserted with NO lease held so PickReady is purely
	// capability-gated.
	// -------------------------------------------------------------------------
	t.Run("capability_routing", func(t *testing.T) {
		linuxPick, err := agentLinux.PickReady(ctx, projectID)
		if err != nil {
			t.Fatalf("host-linux PickReady: %v", err)
		}
		if linuxPick.ID != "T-generic" {
			t.Fatalf("host-linux must pick T-generic (cannot run ios-build), picked %q", linuxPick.ID)
		}

		macPick, err := agentMac.PickReady(ctx, projectID)
		if err != nil {
			t.Fatalf("host-mac PickReady: %v", err)
		}
		// host-mac can run BOTH; lowest ID wins deterministically → T-generic. The
		// load-bearing claim is that host-mac CAN reach T-ios while host-linux cannot,
		// proven below by removing T-generic from contention.
		if macPick.ID != "T-generic" {
			t.Fatalf("host-mac PickReady should pick lowest-ID pickable (T-generic), picked %q", macPick.ID)
		}

		// Make T-generic non-pickable (done) so T-ios is the only ready task. Now
		// host-mac picks T-ios; host-linux finds NOTHING (it cannot satisfy ios-build).
		gt, _ := store.GetTask(ctx, "T-generic")
		gt.Status = registry.StatusDone
		if err := store.UpdateTask(ctx, gt); err != nil {
			t.Fatalf("mark T-generic done: %v", err)
		}
		defer func() { // restore for later subtests.
			gt.Status = registry.StatusReady
			if err := store.UpdateTask(ctx, gt); err != nil {
				t.Fatalf("restore T-generic: %v", err)
			}
		}()

		macPick2, err := agentMac.PickReady(ctx, projectID)
		if err != nil {
			t.Fatalf("host-mac PickReady (T-ios only): %v", err)
		}
		if macPick2.ID != "T-ios" {
			t.Fatalf("host-mac must pick T-ios when it is the only ready task, picked %q", macPick2.ID)
		}
		if _, err := agentLinux.PickReady(ctx, projectID); !errors.Is(err, statestore.ErrNotFound) {
			t.Fatalf("host-linux must find NOTHING pickable (T-ios needs ios-build), got %v", err)
		}
	})

	// -------------------------------------------------------------------------
	// (1) Host-spanning single-winner lease (N-4): both agents race to lease the
	// SAME repo at once. Exactly one wins; the other gets ErrLeaseHeld. Mirrors the
	// statestore N-4 concurrent-acquire proof at the AGENT level.
	// -------------------------------------------------------------------------
	t.Run("single_winner_lease", func(t *testing.T) {
		// Ensure clean slate.
		if err := store.ReleaseLease(ctx, projectID); err != nil {
			t.Fatalf("pre-release: %v", err)
		}

		var winners int64
		var held int64
		var start sync.WaitGroup
		var done sync.WaitGroup
		start.Add(1)
		done.Add(2)

		race := func(agent *registry.Registry, host, taskID string) {
			defer done.Done()
			start.Wait()
			err := agent.AcquireLease(ctx, statestore.Lease{ProjectID: projectID, HostID: host, TaskID: taskID, AcquiredAt: now})
			switch {
			case err == nil:
				atomic.AddInt64(&winners, 1)
			case errors.Is(err, statestore.ErrLeaseHeld):
				atomic.AddInt64(&held, 1)
			default:
				t.Errorf("unexpected AcquireLease error for %s: %v", host, err)
			}
		}
		go race(agentLinux, hostLinux, "T-generic")
		go race(agentMac, hostMac, "T-ios")
		start.Done()
		done.Wait()

		if winners != 1 {
			t.Fatalf("host-spanning single-winner violated: winners=%d want 1 (held=%d)", winners, held)
		}
		if held != 1 {
			t.Fatalf("loser must get ErrLeaseHeld: held=%d want 1", held)
		}
		// Exactly one lease row exists; the other agent did NOT double-write.
		leases, err := store.ListLeases(ctx)
		if err != nil {
			t.Fatalf("ListLeases: %v", err)
		}
		if len(leases) != 1 {
			t.Fatalf("exactly one lease must exist after race, got %d", len(leases))
		}
		// While the lease is held, NEITHER agent can pick (repo busy).
		if _, err := agentLinux.PickReady(ctx, projectID); !errors.Is(err, statestore.ErrNotFound) {
			t.Fatalf("lease held → host-linux PickReady must be empty, got %v", err)
		}
		if _, err := agentMac.PickReady(ctx, projectID); !errors.Is(err, statestore.ErrNotFound) {
			t.Fatalf("lease held → host-mac PickReady must be empty, got %v", err)
		}
	})

	// -------------------------------------------------------------------------
	// (3) Cross-host reap (2B-3): host-mac dies (stale heartbeat) while holding the
	// lease. The host-heartbeat OwnerLive reaps the stale-host lease but KEEPS a
	// fresh host's lease. After the reap a capable host re-acquires the repo.
	// -------------------------------------------------------------------------
	t.Run("cross_host_reap", func(t *testing.T) {
		const hostStale = 90 * time.Second

		// Put the lease on host-mac, then mark host-mac dead (heartbeat 5m stale) and
		// host-linux fresh. Use a clean lease state from the previous subtest.
		if err := store.ReleaseLease(ctx, projectID); err != nil {
			t.Fatalf("pre-release: %v", err)
		}
		if err := store.AcquireLease(ctx, statestore.Lease{ProjectID: projectID, HostID: hostMac, TaskID: "T-ios", AcquiredAt: now}); err != nil {
			t.Fatalf("host-mac acquire: %v", err)
		}
		// host-mac stopped heartbeating; host-linux is alive.
		if err := store.HostHeartbeat(ctx, hostMac, now.Add(-5*time.Minute)); err != nil {
			t.Fatalf("stale host-mac heartbeat: %v", err)
		}
		if err := store.HostHeartbeat(ctx, hostLinux, now); err != nil {
			t.Fatalf("fresh host-linux heartbeat: %v", err)
		}

		// Reconcile with a LONG TTL so ONLY the host-heartbeat OwnerLive can free the
		// lease (isolates the cross-host predicate from the TTL backstop).
		ol := reconcile.HostHeartbeatOwnerLive(ctx, store, hostStale, now)
		rec := reconcile.New(store, twoHostNoCommits{}, reconcile.Config{LeaseTTL: time.Hour, OwnerLive: ol})
		if err := rec.ReapLeases(ctx, now); err != nil {
			t.Fatalf("ReapLeases: %v", err)
		}

		// Stale-host lease freed.
		if _, err := store.GetLease(ctx, projectID); err == nil {
			t.Fatalf("dead host-mac lease must be reaped (stale heartbeat)")
		}

		// A capable host can now re-acquire the freed repo (host-mac on restart; or,
		// for a non-iOS task, host-linux). Prove host-mac re-acquires.
		if err := store.AcquireLease(ctx, statestore.Lease{ProjectID: projectID, HostID: hostMac, TaskID: "T-ios", AcquiredAt: now.Add(time.Second)}); err != nil {
			t.Fatalf("re-acquire after reap must succeed: %v", err)
		}

		// FRESH host's lease is NOT reaped: give host-linux a lease on another repo and
		// confirm a reap pass leaves it untouched.
		if err := store.CreateProject(ctx, statestore.Project{ID: "proj-fresh", Repo: "o/r", BaseBranch: "develop"}); err != nil {
			t.Fatalf("seed proj-fresh: %v", err)
		}
		if err := store.AcquireLease(ctx, statestore.Lease{ProjectID: "proj-fresh", HostID: hostLinux, TaskID: "T-generic", AcquiredAt: now}); err != nil {
			t.Fatalf("host-linux acquire proj-fresh: %v", err)
		}
		ol2 := reconcile.HostHeartbeatOwnerLive(ctx, store, hostStale, now)
		rec2 := reconcile.New(store, twoHostNoCommits{}, reconcile.Config{LeaseTTL: time.Hour, OwnerLive: ol2})
		if err := rec2.ReapLeases(ctx, now); err != nil {
			t.Fatalf("ReapLeases #2: %v", err)
		}
		if _, err := store.GetLease(ctx, "proj-fresh"); err != nil {
			t.Fatalf("FRESH host-linux lease must be kept across a reap, got %v", err)
		}
	})
}

// twoHostNoCommits is a no-op GitReader: this proof exercises leasing/routing/reap,
// not task reconcile, so the base branch has no commits to derive.
type twoHostNoCommits struct{}

func (twoHostNoCommits) BaseCommits(_ context.Context, _ statestore.Project) ([]reconcile.Commit, error) {
	return nil, nil
}
