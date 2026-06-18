package conductor

import (
	"context"
	"errors"
	"testing"

	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/registry"
	"github.com/everva/conductor-platform/internal/statestore"
)

// TestStorePauser_PersistsAcrossFreshReader proves the pause is DURABLE in the
// shared store, not an in-process flag: a pause written through one StorePauser is
// visible to a SEPARATE StorePauser (and a fresh registry) reading the same store
// — the cross-process property the daemon relies on. Resume clears it.
func TestStorePauser_PersistsAcrossFreshReader(t *testing.T) {
	ctx := context.Background()
	store := statestore.NewMemoryStore()
	if err := store.CreateProject(ctx, statestore.Project{ID: projectID, Repo: "owner/repo", BaseBranch: "develop"}); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	writer := NewStorePauser(store)
	if paused, err := writer.Paused(ctx, projectID); err != nil || paused {
		t.Fatalf("fresh project must be unpaused: paused=%v err=%v", paused, err)
	}

	if err := writer.Pause(ctx, projectID); err != nil {
		t.Fatalf("pause: %v", err)
	}

	// A SEPARATE reader over the SAME store (the daemon's view) sees the pause.
	reader := NewStorePauser(store)
	if paused, err := reader.Paused(ctx, projectID); err != nil || !paused {
		t.Fatalf("separate reader must see persisted pause: paused=%v err=%v", paused, err)
	}

	// Idempotent double-pause stays paused.
	if err := writer.Pause(ctx, projectID); err != nil {
		t.Fatalf("double pause: %v", err)
	}
	if paused, _ := reader.Paused(ctx, projectID); !paused {
		t.Fatalf("double pause must stay paused")
	}

	// Resume clears it for the separate reader too; idempotent.
	if err := writer.Resume(ctx, projectID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if paused, err := reader.Paused(ctx, projectID); err != nil || paused {
		t.Fatalf("resume must clear pause for separate reader: paused=%v err=%v", paused, err)
	}
	if err := writer.Resume(ctx, projectID); err != nil {
		t.Fatalf("double resume: %v", err)
	}
	if paused, _ := reader.Paused(ctx, projectID); paused {
		t.Fatalf("double resume must stay running")
	}
}

// TestStorePauser_PauseCreatesNoTask proves pause is now a first-class Project
// run-state (ADR-0021), NOT a task: pausing must NOT create any task and the
// project's ledger is unchanged. This replaces the now-moot ADR-0020
// marker-invisibility test (there is no marker task anymore).
func TestStorePauser_PauseCreatesNoTask(t *testing.T) {
	ctx := context.Background()
	store := statestore.NewMemoryStore()
	if err := store.CreateProject(ctx, statestore.Project{ID: projectID, Repo: "owner/repo", BaseBranch: "develop"}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := store.CreateTask(ctx, statestore.Task{ID: "A-1", ProjectID: projectID, Status: registry.StatusReady}); err != nil {
		t.Fatalf("seed real task: %v", err)
	}
	if err := NewStorePauser(store).Pause(ctx, projectID); err != nil {
		t.Fatalf("pause: %v", err)
	}

	tasks, err := store.ListTasks(ctx, projectID)
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != "A-1" {
		t.Fatalf("pause must not create or alter any task, got %+v", tasks)
	}
	// And the pause is reflected on the project itself (observable, first-class).
	proj, err := store.GetProject(ctx, projectID)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if !proj.Paused {
		t.Fatalf("pause must set Project.Paused=true, got %+v", proj)
	}
}

// TestStorePauser_UnknownProjectErrors proves pausing an unknown project surfaces
// a clear error (wrapped ErrNotFound) rather than silently succeeding.
func TestStorePauser_UnknownProjectErrors(t *testing.T) {
	ctx := context.Background()
	store := statestore.NewMemoryStore()
	if err := NewStorePauser(store).Pause(ctx, "ghost"); !errors.Is(err, statestore.ErrNotFound) {
		t.Fatalf("pause unknown project: err = %v, want ErrNotFound", err)
	}
}

// pauseHarness wires a Conductor with an injected StorePauser over the same store
// that holds the seeded project + ready task, so the pause-gate can be driven.
func pauseHarness(t *testing.T) *harness {
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
		eng:   &fakeEngine{verdict: engine.Verdict{Result: "pass"}},
		prov:  &fakeProvisioner{},
		verf:  &fakeVerifier{result: reviewPass},
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
		Pauser:      NewStorePauser(store),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h.cond = cond
	return h
}

// TestTick_Paused_IsCleanNoOp proves a paused project's tick is a clean no-op:
// OutcomePaused, NO lease taken, NO develop, NO merge — and resume re-enables the
// merge path. This is the end-to-end reverse-channel behavior the daemon honors.
func TestTick_Paused_IsCleanNoOp(t *testing.T) {
	ctx := context.Background()
	h := pauseHarness(t)
	pauser := NewStorePauser(h.store)

	// Pause, then tick: must no-op with no side effects.
	if err := pauser.Pause(ctx, projectID); err != nil {
		t.Fatalf("pause: %v", err)
	}
	res, err := h.cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("tick (paused): %v", err)
	}
	if res.Outcome != OutcomePaused {
		t.Fatalf("paused tick outcome = %q, want %q", res.Outcome, OutcomePaused)
	}
	if h.leaseHeld(t) {
		t.Fatalf("paused tick must take NO lease")
	}
	if h.eng.developed != 0 {
		t.Fatalf("paused tick must NOT develop, got %d develop calls", h.eng.developed)
	}
	if h.merge.calls != 0 {
		t.Fatalf("paused tick must NOT merge, got %d merge calls", h.merge.calls)
	}
	if got := h.task(t).Status; got != registry.StatusReady {
		t.Fatalf("paused task status changed to %q, want it left ready", got)
	}

	// Resume, then tick: the merge path runs again (proceeds exactly as unpaused).
	if err := pauser.Resume(ctx, projectID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	res, err = h.cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("tick (resumed): %v", err)
	}
	if res.Outcome != OutcomeMerged {
		t.Fatalf("resumed tick outcome = %q, want %q", res.Outcome, OutcomeMerged)
	}
	if h.eng.developed != 1 || h.merge.calls != 1 {
		t.Fatalf("resumed tick must develop+merge once: develop=%d merge=%d", h.eng.developed, h.merge.calls)
	}
}

// TestTick_NilPauser_NeverPaused proves backward compatibility: with no Pauser
// injected (the pre-P3-3 wiring), a tick proceeds to merge exactly as before even
// when the project's run-state is paused in the store.
func TestTick_NilPauser_NeverPaused(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, engine.Verdict{Result: "pass"}, nil, reviewPass, nil) // no Pauser in Deps
	if err := NewStorePauser(h.store).Pause(ctx, projectID); err != nil {
		t.Fatalf("pause: %v", err)
	}
	res, err := h.cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if res.Outcome != OutcomeMerged {
		t.Fatalf("nil-pauser tick must ignore pause marker and merge, got %q", res.Outcome)
	}
}
