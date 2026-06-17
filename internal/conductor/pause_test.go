package conductor

import (
	"context"
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

// TestStorePauser_MarkerInvisibleToLedger proves the pause marker never pollutes
// the real project's ledger: ListTasks for the project returns only the real
// task, not the reserved marker (its ProjectID is empty by construction).
func TestStorePauser_MarkerInvisibleToLedger(t *testing.T) {
	ctx := context.Background()
	store := statestore.NewMemoryStore()
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
		t.Fatalf("pause marker must not surface in the project ledger, got %+v", tasks)
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
// when a pause marker happens to exist in the store for the project.
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
