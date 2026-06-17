package conductor

import (
	"context"
	"sync"
	"testing"

	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/events"
	"github.com/everva/conductor-platform/internal/registry"
	"github.com/everva/conductor-platform/internal/statestore"
)

// recordingEmitter captures published events for assertions.
type recordingEmitter struct {
	mu   sync.Mutex
	evts []events.Event
}

func (r *recordingEmitter) Publish(_ context.Context, ev events.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.evts = append(r.evts, ev)
	return nil
}

func (r *recordingEmitter) kinds() []events.Kind {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]events.Kind, len(r.evts))
	for i, e := range r.evts {
		out[i] = e.Kind
	}
	return out
}

func (r *recordingEmitter) hasKind(k events.Kind) bool {
	for _, got := range r.kinds() {
		if got == k {
			return true
		}
	}
	return false
}

// condWithEmitter builds a conductor whose tick merges a task, wired with the
// given emitter so we can assert lifecycle events without disturbing the shared
// harness.
func condWithEmitter(t *testing.T, em Emitter, verdict engine.Verdict, developErr error, reviewResult string) (*Conductor, *statestore.MemoryStore) {
	t.Helper()
	ctx := context.Background()
	store := statestore.NewMemoryStore()
	if err := store.CreateProject(ctx, statestore.Project{ID: projectID, Repo: "owner/repo", BaseBranch: "develop"}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := store.CreateTask(ctx, statestore.Task{ID: "T-1", ProjectID: projectID, Lane: "x", Tier: "T2", Status: registry.StatusReady, Branch: "conductor/T-1"}); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	cond, err := New(Deps{
		Store:       store,
		Picker:      registry.NewRegistry(store),
		Provisioner: &fakeProvisioner{},
		Engine:      &fakeEngine{verdict: verdict, developErr: developErr},
		Verifier:    &fakeVerifier{result: reviewResult},
		Merger:      &fakeMerger{sha: "deadbeef"},
		HostID:      "host-1",
		Emitter:     em,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return cond, store
}

func TestTickEmitsLifecycleEventsOnMerge(t *testing.T) {
	em := &recordingEmitter{}
	cond, _ := condWithEmitter(t, em, engine.Verdict{Result: "pass"}, nil, "pass")

	res, err := cond.Tick(context.Background(), projectID)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if res.Outcome != OutcomeMerged {
		t.Fatalf("expected merged, got %s", res.Outcome)
	}
	for _, want := range []events.Kind{events.KindStarted, events.KindMerge} {
		if !em.hasKind(want) {
			t.Errorf("expected a %q event, got kinds %v", want, em.kinds())
		}
	}
	// All emitted events must validate (correct phase/kind/project).
	em.mu.Lock()
	defer em.mu.Unlock()
	for _, e := range em.evts {
		if err := e.Validate(); err != nil {
			t.Errorf("emitted event invalid: %+v: %v", e, err)
		}
		if e.Project != projectID || e.Task != "T-1" {
			t.Errorf("emitted event missing project/task: %+v", e)
		}
	}
}

func TestTickEmitsInterventionNeededOnAuthExpired(t *testing.T) {
	em := &recordingEmitter{}
	cond, _ := condWithEmitter(t, em, engine.Verdict{}, engine.ErrAuthExpired, "")

	res, err := cond.Tick(context.Background(), projectID)
	if err == nil {
		t.Fatal("expected an error on auth-expired stop")
	}
	if res.Outcome != OutcomeStopped {
		t.Fatalf("expected stopped, got %s", res.Outcome)
	}
	if !em.hasKind(events.KindInterventionNeeded) {
		t.Errorf("expected intervention-needed event, got kinds %v", em.kinds())
	}
}

func TestTickNilEmitterIsNoOp(t *testing.T) {
	// A nil emitter must not break the tick (backward-compatible wiring).
	cond, _ := condWithEmitter(t, nil, engine.Verdict{Result: "pass"}, nil, "pass")
	res, err := cond.Tick(context.Background(), projectID)
	if err != nil {
		t.Fatalf("tick with nil emitter: %v", err)
	}
	if res.Outcome != OutcomeMerged {
		t.Fatalf("expected merged, got %s", res.Outcome)
	}
}
