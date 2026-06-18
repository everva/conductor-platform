// C-1 regression tests: the host-registry heartbeat is driven by a DEDICATED
// background goroutine (runHostHeartbeat), decoupled from the tick body, so a LIVE
// host running a long develop is never false-reaped by the reconcile reaper
// (HostHeartbeatOwnerLive). Before the fix the heartbeat advanced only AFTER each
// (up to -timeout 30m) tick returned, so a busy host went stale within host-stale
// (2m default) and another host could acquire the same repo — a repo-per-1
// (ADR-0008) violation. These tests inject a clock and prove the goroutine fires
// independent of tick progress, while a genuinely dead host (no goroutine) is still
// reaped. Hermetic: no real DB, no real `claude -p`, no wall-clock sleeps gating
// the assertion logic.
package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/everva/conductor-platform/internal/reconcile"
	"github.com/everva/conductor-platform/internal/statestore"
)

// TestHostHeartbeat_BackgroundGoroutine_KeepsBusyHostLive is the core C-1 proof. A
// host's develop is "in progress" (modelled by a blocking gate that NEVER returns
// for the duration of the assertion — i.e. the tick body is stuck, exactly the long
// develop that starved the old per-tick heartbeat). Meanwhile the dedicated
// background heartbeat goroutine advances the host's LastHeartbeat on its own
// cadence. We drive a simulated wall clock FORWARD past several host-stale windows
// and, at each step, assert HostHeartbeatOwnerLive still sees the host as LIVE — so
// the reconcile reaper would NOT release its active lease. The heartbeat advancing
// while the develop is blocked is the whole point: it fires independent of tick
// progress.
func TestHostHeartbeat_BackgroundGoroutine_KeepsBusyHostLive(t *testing.T) {
	ctx := context.Background()
	const (
		hostID    = "agent-busy"
		projectID = "proj-busy"
		taskID    = "T-long-develop"
		hostStale = 2 * time.Minute // reconcile default
	)

	s := statestore.NewMemoryStore()
	if err := s.RegisterHost(ctx, statestore.Host{ID: hostID}); err != nil {
		t.Fatalf("register host: %v", err)
	}

	// A simulated wall clock the heartbeat goroutine stamps with. It advances under
	// our control (not real time), so the heartbeat writes increasing timestamps
	// while a develop is "blocked" — proving the goroutine is not gated on the tick.
	var nowNanos atomic.Int64
	base := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	nowNanos.Store(base.UnixNano())
	hbClock := func() time.Time { return time.Unix(0, nowNanos.Load()).UTC() }

	// The host holds an ACTIVE lease for its long develop, acquired "now". The TTL
	// backstop is long, so ONLY the host-heartbeat OwnerLive predicate can free it —
	// isolating C-1 (a live-but-busy host must stay live by its heartbeat alone).
	if err := s.AcquireLease(ctx, statestore.Lease{ProjectID: projectID, HostID: hostID, TaskID: taskID, AcquiredAt: base}); err != nil {
		t.Fatalf("acquire lease: %v", err)
	}

	// Build the daemon by hand (no flag parsing) with a TINY heartbeat interval and
	// the injected clock, so the goroutine fires many times quickly under test.
	d := &Daemon{
		logger:     newTestLogger(),
		store:      s,
		hostID:     hostID,
		hbInterval: time.Millisecond,
		hbClock:    hbClock,
	}

	// "Develop in progress": a goroutine that blocks until the test signals done. It
	// stands in for the long (up to -timeout) develop that would have starved the
	// old per-tick heartbeat. It NEVER advances the heartbeat itself.
	developDone := make(chan struct{})
	var developWG sync.WaitGroup
	developWG.Add(1)
	go func() {
		defer developWG.Done()
		<-developDone // block, like a long develop holding the tick body.
	}()

	// Start the background heartbeat goroutine (what Run does in loop mode).
	hbCtx, stopHB := context.WithCancel(ctx)
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		d.runHostHeartbeat(hbCtx)
	}()

	ownerLiveAt := func(simNow time.Time) bool {
		pred := reconcile.HostHeartbeatOwnerLive(ctx, s, hostStale, simNow)
		l, err := s.GetLease(ctx, projectID)
		if err != nil {
			t.Fatalf("get lease: %v", err)
		}
		return pred(l)
	}

	// Advance the simulated clock far past multiple host-stale windows. At each step
	// we move the heartbeat clock forward, let the goroutine write a fresh beat at
	// the new "now", then assert the reaper (evaluating OwnerLive at that same now)
	// still sees the host LIVE. The develop is still blocked the entire time.
	for i := 1; i <= 10; i++ {
		simNow := base.Add(time.Duration(i) * hostStale) // 2m, 4m, ... 20m of "develop".
		nowNanos.Store(simNow.UnixNano())

		// Wait until the goroutine has stamped a heartbeat at (or after) simNow.
		if !waitFor(t, time.Second, func() bool {
			h, err := s.GetHost(ctx, hostID)
			if err != nil {
				t.Fatalf("get host: %v", err)
			}
			return !h.LastHeartbeat.Before(simNow)
		}) {
			t.Fatalf("step %d: background heartbeat did not advance to simNow=%v while develop blocked", i, simNow)
		}

		if !ownerLiveAt(simNow) {
			h, _ := s.GetHost(ctx, hostID)
			t.Fatalf("step %d: live-but-busy host wrongly judged DEAD at simNow=%v (LastHeartbeat=%v) — C-1 regression",
				i, simNow, h.LastHeartbeat)
		}
	}

	// Tear down: end the develop and stop the heartbeat goroutine; it must exit on
	// ctx cancel (no leak).
	close(developDone)
	developWG.Wait()
	stopHB()
	select {
	case <-hbDone:
	case <-time.After(time.Second):
		t.Fatal("runHostHeartbeat did not return on ctx cancel (goroutine leak)")
	}
}

// TestHostHeartbeat_DeadHostStillReaped is the negative half of C-1: a host with NO
// running heartbeat goroutine (a genuinely dead daemon) goes stale and IS reaped.
// This proves the fix did not blunt the reaper — only a host that keeps
// heartbeating survives. We register the host, write ONE heartbeat, then never
// advance it; evaluated host-stale later, OwnerLive reports DEAD (reapable).
func TestHostHeartbeat_DeadHostStillReaped(t *testing.T) {
	ctx := context.Background()
	const (
		hostID    = "agent-dead"
		projectID = "proj-dead"
		hostStale = 2 * time.Minute
	)
	base := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

	s := statestore.NewMemoryStore()
	if err := s.RegisterHost(ctx, statestore.Host{ID: hostID}); err != nil {
		t.Fatalf("register host: %v", err)
	}
	// One heartbeat at base, then the daemon "dies" — no goroutine advances it.
	if err := s.HostHeartbeat(ctx, hostID, base); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if err := s.AcquireLease(ctx, statestore.Lease{ProjectID: projectID, HostID: hostID, TaskID: "T-x", AcquiredAt: base}); err != nil {
		t.Fatalf("acquire lease: %v", err)
	}

	// Within host-stale → still LIVE.
	livePred := reconcile.HostHeartbeatOwnerLive(ctx, s, hostStale, base.Add(time.Minute))
	l, err := s.GetLease(ctx, projectID)
	if err != nil {
		t.Fatalf("get lease: %v", err)
	}
	if !livePred(l) {
		t.Fatal("host within host-stale must be LIVE")
	}

	// Past host-stale with no further heartbeat → DEAD (reapable). This is the
	// reaper correctly freeing a truly dead host's lease.
	deadPred := reconcile.HostHeartbeatOwnerLive(ctx, s, hostStale, base.Add(3*time.Minute))
	if deadPred(l) {
		t.Fatal("dead host (no heartbeat goroutine) past host-stale must be reapable")
	}
}

// waitFor polls cond until it is true or the timeout elapses, returning whether it
// became true. It lets a test wait on the background goroutine's effect without a
// fixed sleep (which would be flaky), while staying offline.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return cond()
}
