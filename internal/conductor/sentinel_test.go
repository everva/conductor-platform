package conductor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/registry"
	"github.com/everva/conductor-platform/internal/sentinel"
	"github.com/everva/conductor-platform/internal/statestore"
)

// stubDecider is a deterministic SentinelDecider for the offline conductor tests:
// it returns a fixed Result on every Assess, recording the call count so a test
// can assert the watchdog actually consulted it. No `claude -p` runs.
type stubDecider struct {
	result sentinel.Result
	calls  int
}

func (s *stubDecider) Assess(_ context.Context, _ sentinel.Signals) sentinel.Result {
	s.calls++
	return s.result
}

// constProbe always returns the same fixed signals (the watchdog decision is
// driven by the stub decider, not the signals, so the probe is a constant). A
// non-nil probe avoids the default engine-Health probe and keeps the test
// hermetic.
func constProbe() ProgressProbe {
	return ProgressProbeFunc(func(_ context.Context, _, _ time.Time) (sentinel.Signals, error) {
		return sentinel.Signals{Elapsed: time.Hour, SinceActivity: time.Hour, Alive: true}, nil
	})
}

// sentinelHarness wires a Conductor with the blocking engine (blocks until its ctx
// is cancelled) + an injected SentinelDecider + constant probe, over a shared store
// holding a seeded ready task. The blocking engine makes the test DETERMINISTIC:
// develop only returns when the watchdog cancels the child context, so the outcome
// is decision-driven, not timing-driven.
func sentinelHarness(t *testing.T, dec *stubDecider) (*Conductor, *statestore.MemoryStore, *blockingEngine, *fakeMerger, *fakeVerifier) {
	t.Helper()
	ctx := context.Background()
	store := statestore.NewMemoryStore()
	if err := store.CreateProject(ctx, statestore.Project{ID: projectID, Repo: "owner/repo", BaseBranch: "develop"}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := store.CreateTask(ctx, statestore.Task{ID: "T-1", ProjectID: projectID, Lane: "x", Tier: "T2", Status: registry.StatusReady, Branch: "conductor/T-1"}); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	eng := newBlockingEngine()
	merge := &fakeMerger{sha: "deadbeef"}
	verf := &fakeVerifier{result: reviewPass}
	cond, err := New(Deps{
		Store:        store,
		Picker:       registry.NewRegistry(store),
		Provisioner:  &fakeProvisioner{},
		Engine:       eng,
		Verifier:     verf,
		Merger:       merge,
		HostID:       "host-1",
		Sentinel:     dec,
		Probe:        constProbe(),
		SentinelPoll: 2 * time.Millisecond, // responsive; the outcome is decision-driven.
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return cond, store, eng, merge, verf
}

// TestTick_Sentinel_Stuck_KillsDevelop_BlocksTask is the core conductor 2C-1 test:
// a stalling develop (blocked on its ctx) is assessed as Kill (gray-zone "stuck"
// upstream); the watchdog cancels develop, the tick runs NO verify and NO merge,
// and the task is BLOCKED (sentinel-killed) — never fake-green.
func TestTick_Sentinel_Stuck_KillsDevelop_BlocksTask(t *testing.T) {
	ctx := context.Background()
	dec := &stubDecider{result: sentinel.Result{Decision: sentinel.Kill, Layer: 2, Advice: sentinel.AdviceStuck, Reason: "stuck"}}
	cond, store, eng, merge, verf := sentinelHarness(t, dec)

	res, err := cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("tick (sentinel kill): %v", err)
	}
	if res.Outcome != OutcomeSentinelKilled {
		t.Fatalf("outcome = %q, want %q", res.Outcome, OutcomeSentinelKilled)
	}
	if !eng.cancelled {
		t.Fatalf("develop must have been cancelled by the sentinel watchdog")
	}
	if dec.calls == 0 {
		t.Fatalf("watchdog must have consulted the decider")
	}
	if verf.calls != 0 {
		t.Fatalf("sentinel-killed tick must NOT verify, got %d", verf.calls)
	}
	if merge.calls != 0 {
		t.Fatalf("sentinel-killed tick must NOT merge, got %d", merge.calls)
	}
	task, err := store.GetTask(ctx, "T-1")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.Status != statusBlocked {
		t.Fatalf("sentinel-killed task status = %q, want blocked", task.Status)
	}
	if _, err := store.GetLease(ctx, projectID); !errors.Is(err, statestore.ErrNotFound) {
		t.Fatalf("sentinel-killed tick must release the lease, GetLease err = %v", err)
	}
}

// TestTick_Sentinel_Escalate_KillsDevelop_BlocksTask proves a gray-zone
// needs_human verdict (mapped to Escalate) also cancels develop and blocks the
// task (with the human-needed reason) — no verify, no merge.
func TestTick_Sentinel_Escalate_KillsDevelop_BlocksTask(t *testing.T) {
	ctx := context.Background()
	dec := &stubDecider{result: sentinel.Result{Decision: sentinel.Escalate, Layer: 2, Advice: sentinel.AdviceNeedsHuman, Reason: "needs human"}}
	cond, store, eng, merge, verf := sentinelHarness(t, dec)

	res, err := cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("tick (sentinel escalate): %v", err)
	}
	if res.Outcome != OutcomeSentinelKilled {
		t.Fatalf("outcome = %q, want %q", res.Outcome, OutcomeSentinelKilled)
	}
	if !eng.cancelled || verf.calls != 0 || merge.calls != 0 {
		t.Fatalf("escalate must cancel develop and skip verify/merge: cancelled=%v verify=%d merge=%d", eng.cancelled, verf.calls, merge.calls)
	}
	task, _ := store.GetTask(ctx, "T-1")
	if task.Status != statusBlocked {
		t.Fatalf("escalated task status = %q, want blocked", task.Status)
	}
}

// TestTick_Sentinel_BackstopProgressing_StillKills is THE DF-difference test at
// the conductor level: the sentinel decider returns the Layer-3 backstop Kill even
// though the (upstream) advisor said "progressing" — the watchdog kills the
// stalling develop anyway and the task is blocked. The advisor CANNOT override the
// backstop. The decider here models exactly what *sentinel.Sentinel.Assess
// returns when Elapsed >= MaxTotal with a progressing advisor (Layer 3 Kill,
// advisor never consulted), proving the conductor honors the backstop's override.
func TestTick_Sentinel_BackstopProgressing_StillKills(t *testing.T) {
	ctx := context.Background()
	// Layer-3 backstop Kill: this is precisely the Result a real Sentinel produces
	// when elapsed reached MaxTotal while the advisor would have said progressing —
	// the backstop is checked FIRST and binds, so Layer is 3 and Advice is empty.
	dec := &stubDecider{result: sentinel.Result{Decision: sentinel.Kill, Layer: 3, Reason: "backstop: elapsed reached ceiling; killing regardless of advisor"}}
	cond, store, eng, merge, verf := sentinelHarness(t, dec)

	res, err := cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("tick (backstop): %v", err)
	}
	if res.Outcome != OutcomeSentinelKilled {
		t.Fatalf("BACKSTOP: outcome = %q, want %q (backstop must kill past the ceiling)", res.Outcome, OutcomeSentinelKilled)
	}
	if !eng.cancelled {
		t.Fatalf("BACKSTOP: develop must be killed even though the advisor said progressing")
	}
	if verf.calls != 0 || merge.calls != 0 {
		t.Fatalf("BACKSTOP-killed tick must NOT verify or merge: verify=%d merge=%d", verf.calls, merge.calls)
	}
	task, _ := store.GetTask(ctx, "T-1")
	if task.Status != statusBlocked {
		t.Fatalf("BACKSTOP: task status = %q, want blocked", task.Status)
	}
}

// TestTick_Sentinel_BackstopProgressing_RealSentinel wires a REAL
// *sentinel.Sentinel (not a stub decider) with a stub advisor that says
// "progressing", and a probe whose Elapsed has reached MaxTotal. It proves the
// END-TO-END DF-difference through the actual Assess logic: the advisor says keep
// going, the backstop says stop, and stop wins — the develop is killed and the
// task blocked. This is the load-bearing "advisor progressing but backstop kills"
// integration assertion.
func TestTick_Sentinel_BackstopProgressing_RealSentinel(t *testing.T) {
	ctx := context.Background()
	store := statestore.NewMemoryStore()
	if err := store.CreateProject(ctx, statestore.Project{ID: projectID, Repo: "owner/repo", BaseBranch: "develop"}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := store.CreateTask(ctx, statestore.Task{ID: "T-1", ProjectID: projectID, Lane: "x", Tier: "T2", Status: registry.StatusReady, Branch: "conductor/T-1"}); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	eng := newBlockingEngine()
	merge := &fakeMerger{sha: "deadbeef"}
	verf := &fakeVerifier{result: reviewPass}

	// Real sentinel: MaxTotal small, advisor ALWAYS says progressing.
	advisor := sentinel.AdvisorFunc(func(_ context.Context, _ string) (sentinel.Advice, string, error) {
		return sentinel.AdviceProgressing, "still working, promise", nil
	})
	sent := sentinel.New(sentinel.Config{
		MaxTotal:      50 * time.Millisecond,
		FreshActivity: 1 * time.Millisecond,
		GraceUnsure:   2 * time.Millisecond,
	}, advisor)

	// Probe reports the run as alive, stalled, and ELAPSED PAST the ceiling so the
	// real Assess hits the Layer-3 backstop first (advisor never consulted).
	probe := ProgressProbeFunc(func(_ context.Context, _, _ time.Time) (sentinel.Signals, error) {
		return sentinel.Signals{Elapsed: time.Second, SinceActivity: time.Second, Alive: true, LastOutput: "compiling..."}, nil
	})

	cond, err := New(Deps{
		Store:        store,
		Picker:       registry.NewRegistry(store),
		Provisioner:  &fakeProvisioner{},
		Engine:       eng,
		Verifier:     verf,
		Merger:       merge,
		HostID:       "host-1",
		Sentinel:     sent,
		Probe:        probe,
		SentinelPoll: 2 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if res.Outcome != OutcomeSentinelKilled {
		t.Fatalf("real-sentinel backstop: outcome = %q, want %q (advisor progressing must NOT prevent the kill)", res.Outcome, OutcomeSentinelKilled)
	}
	if !eng.cancelled || merge.calls != 0 {
		t.Fatalf("real-sentinel backstop must kill develop and not merge: cancelled=%v merge=%d", eng.cancelled, merge.calls)
	}
	task, _ := store.GetTask(ctx, "T-1")
	if task.Status != statusBlocked {
		t.Fatalf("real-sentinel backstop: task status = %q, want blocked", task.Status)
	}
}

// TestTick_Sentinel_Continue_LetsDevelopFinishAndMerge proves a Continue decision
// does NOT interfere: the watchdog keeps consulting but never cancels, so a develop
// that completes normally verifies and merges as usual.
func TestTick_Sentinel_Continue_LetsDevelopFinishAndMerge(t *testing.T) {
	ctx := context.Background()
	store := statestore.NewMemoryStore()
	if err := store.CreateProject(ctx, statestore.Project{ID: projectID, Repo: "owner/repo", BaseBranch: "develop"}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := store.CreateTask(ctx, statestore.Task{ID: "T-1", ProjectID: projectID, Lane: "x", Tier: "T2", Status: registry.StatusReady, Branch: "conductor/T-1"}); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	// Non-blocking engine that returns a pass verdict immediately.
	eng := &fakeEngine{verdict: engine.Verdict{Result: "pass"}}
	merge := &fakeMerger{sha: "deadbeef"}
	verf := &fakeVerifier{result: reviewPass}
	dec := &stubDecider{result: sentinel.Result{Decision: sentinel.Continue, Layer: 1}}

	cond, err := New(Deps{
		Store:        store,
		Picker:       registry.NewRegistry(store),
		Provisioner:  &fakeProvisioner{},
		Engine:       eng,
		Verifier:     verf,
		Merger:       merge,
		HostID:       "host-1",
		Sentinel:     dec,
		Probe:        constProbe(),
		SentinelPoll: 2 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("tick (continue): %v", err)
	}
	if res.Outcome != OutcomeMerged {
		t.Fatalf("continue: outcome = %q, want %q", res.Outcome, OutcomeMerged)
	}
	if merge.calls != 1 {
		t.Fatalf("continue: merge calls = %d, want 1", merge.calls)
	}
}

// TestTick_NilSentinel_NoWatchdog proves backward compatibility: with no Sentinel
// injected (today's wiring), a normal develop merges and no watchdog interferes —
// exactly the pre-2C-1 behavior.
func TestTick_NilSentinel_NoWatchdog(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, engine.Verdict{Result: "pass"}, nil, reviewPass, nil) // no Sentinel
	res, err := h.cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if res.Outcome != OutcomeMerged {
		t.Fatalf("nil-sentinel tick must merge as before, got %q", res.Outcome)
	}
}

// TestEngineProgressProbe_DerivesFromHealth proves the default probe maps the
// engine's Health onto Signals: a recent LastActivityTS yields a small
// SinceActivity, elapsed is now-startedAt, and Alive is true.
func TestEngineProgressProbe_DerivesFromHealth(t *testing.T) {
	now := time.Now()
	eng := &healthEngine{hs: engine.HealthState{Signal: "progressing", LastActivityTS: now.Add(-10 * time.Second)}}
	p := engineProgressProbe{eng: eng, session: engine.Session{TaskID: "T-1"}}

	sig, err := p.Probe(context.Background(), now.Add(-time.Minute), now)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if sig.Elapsed != time.Minute {
		t.Fatalf("elapsed = %v, want 1m", sig.Elapsed)
	}
	if sig.SinceActivity != 10*time.Second {
		t.Fatalf("sinceActivity = %v, want 10s", sig.SinceActivity)
	}
	if !sig.Alive {
		t.Fatalf("alive = false, want true")
	}
}

// healthEngine is a minimal engine.EngineAdapter that returns a scripted
// HealthState (only Health is exercised by the probe test).
type healthEngine struct {
	hs engine.HealthState
}

func (e *healthEngine) Develop(context.Context, statestore.Task, engine.Workspace) (engine.Verdict, error) {
	return engine.Verdict{}, nil
}
func (e *healthEngine) Verify(context.Context, engine.Verdict, engine.Workspace) (engine.ReviewResult, error) {
	return engine.ReviewResult{}, nil
}
func (e *healthEngine) Health(context.Context, engine.Session) (engine.HealthState, error) {
	return e.hs, nil
}
func (e *healthEngine) Events(context.Context) (<-chan engine.Event, error) { return nil, nil }
func (e *healthEngine) Control(context.Context, engine.Command) error       { return nil }
