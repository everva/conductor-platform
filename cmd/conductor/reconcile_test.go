// Tests for the INDEPENDENT recovery-job mode (ADR-0016, 2B-5): the -reconcile
// flag parsing (requires -dsn; thresholds parsed) and the testable reconcile-run
// inner function (reconcileRun) driven against an in-memory store with an injected
// clock and a fake git reader — NO os.Exit, no real DB. The pass must reap a
// stale-host lease, keep a fresh-host lease, and mark a [task:<id>]-trailered task
// done (ReapLeases + ReconcileTasks).
package main

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/everva/conductor-platform/internal/reconcile"
	"github.com/everva/conductor-platform/internal/statestore"
)

// fakeReconcileGit is an injected reconcile.GitReader: it returns a fixed set of
// base-branch commits per project so the trailer reconcile needs no real repo
// (decoupled, ADR-0016).
type fakeReconcileGit struct {
	byProject map[string][]reconcile.Commit
}

func (f fakeReconcileGit) BaseCommits(_ context.Context, p statestore.Project) ([]reconcile.Commit, error) {
	return f.byProject[p.ID], nil
}

// TestParseConfig_ReconcileMode proves -reconcile parses, REQUIRES -dsn (errors
// without it), parses -lease-ttl / -host-stale with sensible positive defaults,
// and short-circuits before the daemon's project/root requirements.
func TestParseConfig_ReconcileMode(t *testing.T) {
	t.Run("reconcile needs only dsn (not project/root)", func(t *testing.T) {
		cfg, err := parseConfig([]string{"-reconcile", "-dsn", "postgres://u:p@h:5432/db"}, io.Discard)
		if err != nil {
			t.Fatalf("parseConfig: %v", err)
		}
		if !cfg.reconcile {
			t.Fatalf("reconcile mode not set: %+v", cfg)
		}
		if cfg.dsn != "postgres://u:p@h:5432/db" {
			t.Fatalf("dsn = %q, want the flag value", cfg.dsn)
		}
		if cfg.leaseTTL <= 0 || cfg.hostStale <= 0 {
			t.Fatalf("thresholds should default positive: leaseTTL=%s hostStale=%s", cfg.leaseTTL, cfg.hostStale)
		}
	})

	t.Run("reconcile without dsn errors", func(t *testing.T) {
		// Isolate from an ambient CONDUCTOR_DSN that would satisfy the requirement.
		t.Setenv("CONDUCTOR_DSN", "")
		if _, err := parseConfig([]string{"-reconcile"}, io.Discard); err == nil {
			t.Fatal("expected error: -reconcile requires -dsn")
		}
	})

	t.Run("lease-ttl and host-stale flags parsed", func(t *testing.T) {
		cfg, err := parseConfig([]string{
			"-reconcile", "-dsn", "postgres://h/db",
			"-lease-ttl", "45m", "-host-stale", "90s",
		}, io.Discard)
		if err != nil {
			t.Fatalf("parseConfig: %v", err)
		}
		if cfg.leaseTTL != 45*time.Minute {
			t.Fatalf("leaseTTL = %s, want 45m", cfg.leaseTTL)
		}
		if cfg.hostStale != 90*time.Second {
			t.Fatalf("hostStale = %s, want 90s", cfg.hostStale)
		}
	})

	t.Run("dsn/thresholds from env", func(t *testing.T) {
		t.Setenv("CONDUCTOR_DSN", "postgres://envhost/db")
		t.Setenv("CONDUCTOR_RECONCILE", "true")
		t.Setenv("CONDUCTOR_LEASE_TTL", "10m")
		t.Setenv("CONDUCTOR_HOST_STALE", "30s")
		cfg, err := parseConfig(nil, io.Discard)
		if err != nil {
			t.Fatalf("parseConfig: %v", err)
		}
		if !cfg.reconcile || cfg.dsn != "postgres://envhost/db" {
			t.Fatalf("env reconcile/dsn not applied: %+v", cfg)
		}
		if cfg.leaseTTL != 10*time.Minute || cfg.hostStale != 30*time.Second {
			t.Fatalf("env thresholds not applied: leaseTTL=%s hostStale=%s", cfg.leaseTTL, cfg.hostStale)
		}
	})

	t.Run("non-positive thresholds error", func(t *testing.T) {
		if _, err := parseConfig([]string{"-reconcile", "-dsn", "postgres://h/db", "-lease-ttl", "0"}, io.Discard); err == nil {
			t.Fatal("expected error: -lease-ttl must be positive")
		}
		if _, err := parseConfig([]string{"-reconcile", "-dsn", "postgres://h/db", "-host-stale", "0"}, io.Discard); err == nil {
			t.Fatal("expected error: -host-stale must be positive")
		}
	})
}

// TestReconcileRun_ReapsStaleHostKeepsFreshAndReconcilesTrailers drives the
// testable reconcile-run inner function deterministically: a STALE-host lease (the
// owning host's heartbeat is old) is RELEASED, a FRESH-host lease is KEPT, and a
// committed [task:<id>] trailer marks that task DONE — all in ONE pass with an
// injected clock and a fake git reader (no os.Exit, no real DB).
func TestReconcileRun_ReapsStaleHostKeepsFreshAndReconcilesTrailers(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

	const (
		staleHost = "host-dead"
		freshHost = "host-live"
		projStale = "proj-stale"
		projFresh = "proj-fresh"
	)

	s := statestore.NewMemoryStore()

	// Two projects, one leased by a now-dead host, one by a live host.
	for _, p := range []string{projStale, projFresh} {
		if err := s.CreateProject(ctx, statestore.Project{ID: p, Repo: "o/" + p, BaseBranch: "develop"}); err != nil {
			t.Fatalf("seed project %q: %v", p, err)
		}
	}

	// A task on the stale project that a merge trailer should flip to done.
	const taskID = "T-merged"
	if err := s.CreateTask(ctx, statestore.Task{ID: taskID, ProjectID: projStale, Status: "running"}); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	// Register both hosts; stale host's heartbeat is 5m old, fresh host's is now.
	if err := s.RegisterHost(ctx, statestore.Host{ID: staleHost}); err != nil {
		t.Fatalf("register stale host: %v", err)
	}
	if err := s.RegisterHost(ctx, statestore.Host{ID: freshHost}); err != nil {
		t.Fatalf("register fresh host: %v", err)
	}
	if err := s.HostHeartbeat(ctx, staleHost, now.Add(-5*time.Minute)); err != nil {
		t.Fatalf("stale heartbeat: %v", err)
	}
	if err := s.HostHeartbeat(ctx, freshHost, now); err != nil {
		t.Fatalf("fresh heartbeat: %v", err)
	}

	// Both leases acquired "now" so the TTL backstop alone would NOT reap either —
	// only the host-heartbeat OwnerLive can free the stale-host lease. This isolates
	// the cross-host predicate from the TTL seam.
	if err := s.AcquireLease(ctx, statestore.Lease{ProjectID: projStale, HostID: staleHost, TaskID: taskID, AcquiredAt: now}); err != nil {
		t.Fatalf("acquire stale lease: %v", err)
	}
	if err := s.AcquireLease(ctx, statestore.Lease{ProjectID: projFresh, HostID: freshHost, TaskID: "T-fresh", AcquiredAt: now}); err != nil {
		t.Fatalf("acquire fresh lease: %v", err)
	}

	// Fake git: the stale project's base branch carries a squash-merge commit with
	// the EXACT [task:T-merged] trailer the merger writes.
	git := fakeReconcileGit{byProject: map[string][]reconcile.Commit{
		projStale: {{
			SHA:      "abc123",
			Subject:  "conductor: land T-merged",
			Trailers: []string{"[task:" + taskID + "]"},
		}},
	}}

	// Long TTL so only OwnerLive can reap; host-stale of 2m makes the 5m-old host dead.
	cfg := config{leaseTTL: time.Hour, hostStale: 2 * time.Minute}
	if err := reconcileRun(ctx, s, git, cfg, now, newTestLogger()); err != nil {
		t.Fatalf("reconcileRun: %v", err)
	}

	// Stale-host lease RELEASED.
	if _, err := s.GetLease(ctx, projStale); err == nil {
		t.Fatal("stale-host lease must be reaped (dead host heartbeat)")
	}
	// Fresh-host lease KEPT.
	if _, err := s.GetLease(ctx, projFresh); err != nil {
		t.Fatalf("fresh-host lease must be kept, got %v", err)
	}
	// Trailer → task DONE (ReconcileTasks).
	got, err := s.GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.Status != "done" {
		t.Fatalf("task status = %q, want done (git [task:%s] trailer)", got.Status, taskID)
	}
}

// TestRunReconcile_RequiresDSN proves the top-level run() rejects -reconcile
// without a DSN with the config-error exit code (2), never building the daemon or
// touching a store.
func TestRunReconcile_RequiresDSN(t *testing.T) {
	t.Setenv("CONDUCTOR_DSN", "")
	if code := run(context.Background(), []string{"-reconcile"}, newTestLogger(), io.Discard); code != 2 {
		t.Fatalf("run(-reconcile without -dsn) = %d, want 2", code)
	}
}
