package conductor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/registry"
	"github.com/everva/conductor-platform/internal/statestore"
)

// blockingEngine is a develop runner that BLOCKS until its context is cancelled,
// then returns ctx.Err() — exactly modelling a long-running performer that only
// stops when the abort watcher cancels the develop child context. It makes the
// abort test DETERMINISTIC: the only way Develop returns is a context cancel, so
// the test cannot pass falsely on a wall-clock race. `started` is closed when
// Develop begins so a test can sequence the abort signal after develop is in
// flight; `cancelled` records that develop observed the cancel.
type blockingEngine struct {
	started   chan struct{}
	developed int
	cancelled bool
}

func newBlockingEngine() *blockingEngine {
	return &blockingEngine{started: make(chan struct{})}
}

func (e *blockingEngine) Develop(ctx context.Context, _ statestore.Task, _ engine.Workspace) (engine.Verdict, error) {
	e.developed++
	close(e.started)
	<-ctx.Done() // block until the abort watcher cancels the develop child context.
	e.cancelled = true
	return engine.Verdict{}, ctx.Err()
}
func (e *blockingEngine) Verify(context.Context, engine.Verdict, engine.Workspace) (engine.ReviewResult, error) {
	return engine.ReviewResult{}, nil
}
func (e *blockingEngine) Health(context.Context, engine.Session) (engine.HealthState, error) {
	return engine.HealthState{}, nil
}
func (e *blockingEngine) Events(context.Context) (<-chan engine.Event, error) { return nil, nil }
func (e *blockingEngine) Control(context.Context, engine.Command) error       { return nil }

// abortHarness wires a Conductor with the blocking engine + a real StoreAborter
// over a shared store holding the seeded project + ready task, with a tiny abort
// poll so the watcher reacts promptly. The abort SIGNAL is what drives the test
// (not timing): pre-set or set-by-hook, the watcher cancels the blocked develop.
func abortHarness(t *testing.T) (*Conductor, *statestore.MemoryStore, *blockingEngine, *fakeMerger, *fakeVerifier) {
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
		Store:       store,
		Picker:      registry.NewRegistry(store),
		Provisioner: &fakeProvisioner{},
		Engine:      eng,
		Verifier:    verf,
		Merger:      merge,
		HostID:      "host-1",
		Aborter:     NewStoreAborter(store),
		AbortPoll:   2 * time.Millisecond, // responsive; the outcome is signal-driven.
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return cond, store, eng, merge, verf
}

// TestTick_Abort_CancelsDevelop_RevertsToReady is the DETERMINISTIC core test: an
// in-flight develop (blocked on its ctx) is aborted by an operator signal; the
// watcher cancels the develop, the task is reverted to a SAFE re-runnable state,
// the lease is released, NO verify/NO merge runs, and the abort flag is cleared so
// a re-run is not re-aborted.
func TestTick_Abort_CancelsDevelop_RevertsToReady(t *testing.T) {
	ctx := context.Background()
	cond, store, eng, merge, verf := abortHarness(t)

	// Set the abort signal in a goroutine AFTER develop is in flight (its `started`
	// channel closes), so the watcher observes the flip on a real running develop —
	// the realistic operator sequence. This is signal-driven, not time-driven.
	go func() {
		<-eng.started
		_, _ = NewStoreAborter(store).RequestAbort(ctx, projectID)
	}()

	res, err := cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("tick (abort): %v", err)
	}
	if res.Outcome != OutcomeAborted {
		t.Fatalf("outcome = %q, want %q", res.Outcome, OutcomeAborted)
	}
	if !eng.cancelled {
		t.Fatalf("develop must have been cancelled by the abort watcher")
	}
	if eng.developed != 1 {
		t.Fatalf("develop calls = %d, want 1", eng.developed)
	}
	if verf.calls != 0 {
		t.Fatalf("aborted tick must NOT verify, got %d verify calls", verf.calls)
	}
	if merge.calls != 0 {
		t.Fatalf("aborted tick must NOT merge, got %d merge calls", merge.calls)
	}
	// Task reverted to a SAFE, re-runnable state.
	task, err := store.GetTask(ctx, "T-1")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.Status != registry.StatusReady {
		t.Fatalf("aborted task status = %q, want ready (re-runnable)", task.Status)
	}
	// Abort flag cleared so the re-run is not immediately re-aborted.
	if task.AbortRequested {
		t.Fatalf("aborted task must have its abort flag cleared, got %+v", task)
	}
	// Lease released.
	if _, err := store.GetLease(ctx, projectID); !errors.Is(err, statestore.ErrNotFound) {
		t.Fatalf("aborted tick must release the lease, GetLease err = %v", err)
	}
}

// TestTick_Abort_PreSet_DeterministicNoStartHook proves the abort honoring is fully
// signal-driven even when the flag is pre-set before the tick: with the develop
// blocked on ctx, the watcher reads the pre-set flag and cancels — the tick cannot
// return any other way, so there is no wall-clock race in the assertion.
func TestTick_Abort_PreSet_DeterministicNoStartHook(t *testing.T) {
	ctx := context.Background()
	cond, store, eng, merge, _ := abortHarness(t)

	// Pre-set the abort flag directly on the task (the running-task it will lease).
	task, _ := store.GetTask(ctx, "T-1")
	task.AbortRequested = true
	if err := store.UpdateTask(ctx, task); err != nil {
		t.Fatalf("pre-set abort: %v", err)
	}

	res, err := cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if res.Outcome != OutcomeAborted {
		t.Fatalf("outcome = %q, want %q", res.Outcome, OutcomeAborted)
	}
	if !eng.cancelled || merge.calls != 0 {
		t.Fatalf("pre-set abort must cancel develop and not merge: cancelled=%v merge=%d", eng.cancelled, merge.calls)
	}
}

// TestTick_NilAborter_NoWatcher proves backward compatibility: with no Aborter
// injected (pre-F-2 wiring), a develop runs to completion and merges even if a task
// carries an abort flag — no watcher exists to honor it.
func TestTick_NilAborter_NoWatcher(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, engine.Verdict{Result: "pass"}, nil, reviewPass, nil) // no Aborter
	task, _ := h.store.GetTask(ctx, "T-1")
	task.AbortRequested = true
	if err := h.store.UpdateTask(ctx, task); err != nil {
		t.Fatalf("set abort: %v", err)
	}
	res, err := h.cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if res.Outcome != OutcomeMerged {
		t.Fatalf("nil-aborter tick must ignore the abort flag and merge, got %q", res.Outcome)
	}
}

// --- StoreAborter unit tests ------------------------------------------------

// TestStoreAborter_ResolvesRunningTaskViaLease proves RequestAbort resolves the
// running task through the project's active lease and sets the durable signal on
// THAT task, persisted for a separate reader (the daemon's watcher).
func TestStoreAborter_ResolvesRunningTaskViaLease(t *testing.T) {
	ctx := context.Background()
	store := statestore.NewMemoryStore()
	if err := store.CreateProject(ctx, statestore.Project{ID: projectID, Repo: "owner/repo", BaseBranch: "develop"}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := store.CreateTask(ctx, statestore.Task{ID: "RUN-1", ProjectID: projectID, Status: registry.StatusRunning}); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	// Simulate the conductor holding the repo lease for RUN-1.
	if err := store.AcquireLease(ctx, statestore.Lease{ProjectID: projectID, HostID: "host-1", TaskID: "RUN-1"}); err != nil {
		t.Fatalf("acquire lease: %v", err)
	}

	writer := NewStoreAborter(store)
	taskID, err := writer.RequestAbort(ctx, projectID)
	if err != nil {
		t.Fatalf("RequestAbort: %v", err)
	}
	if taskID != "RUN-1" {
		t.Fatalf("RequestAbort resolved task = %q, want RUN-1 (the leased task)", taskID)
	}

	// A SEPARATE reader (the daemon's view) sees the persisted signal.
	reader := NewStoreAborter(store)
	if req, err := reader.AbortRequested(ctx, "RUN-1"); err != nil || !req {
		t.Fatalf("separate reader must see persisted abort: req=%v err=%v", req, err)
	}

	// ClearAbort makes it one-shot for the separate reader too; idempotent.
	if err := writer.ClearAbort(ctx, "RUN-1"); err != nil {
		t.Fatalf("ClearAbort: %v", err)
	}
	if req, _ := reader.AbortRequested(ctx, "RUN-1"); req {
		t.Fatalf("ClearAbort must clear the signal for the separate reader")
	}
	if err := writer.ClearAbort(ctx, "RUN-1"); err != nil {
		t.Fatalf("idempotent double clear: %v", err)
	}
}

// TestStoreAborter_NothingRunning proves aborting a project with no active lease
// (nothing running) surfaces ErrNothingRunning rather than silently succeeding.
func TestStoreAborter_NothingRunning(t *testing.T) {
	ctx := context.Background()
	store := statestore.NewMemoryStore()
	if err := store.CreateProject(ctx, statestore.Project{ID: projectID, Repo: "owner/repo", BaseBranch: "develop"}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if _, err := NewStoreAborter(store).RequestAbort(ctx, projectID); !errors.Is(err, ErrNothingRunning) {
		t.Fatalf("RequestAbort with no lease: err = %v, want ErrNothingRunning", err)
	}
}
