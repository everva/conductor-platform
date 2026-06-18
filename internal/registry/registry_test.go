package registry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/everva/conductor-platform/internal/statestore"
)

const testProject = "proj-1"

// newStore returns a MemoryStore seeded with a single project. The Registry
// depends on the frozen StateStore interface, but exercising it against the real
// in-memory implementation gives an honest end-to-end check (ADR-0013).
func newStore(t *testing.T) statestore.StateStore {
	t.Helper()
	ctx := context.Background()
	s := statestore.NewMemoryStore()
	if err := s.CreateProject(ctx, statestore.Project{ID: testProject, Repo: "owner/repo", BaseBranch: "develop"}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	return s
}

// seedTasks creates the given tasks in the store, failing the test on error.
func seedTasks(t *testing.T, s statestore.StateStore, tasks ...statestore.Task) {
	t.Helper()
	ctx := context.Background()
	for _, tk := range tasks {
		if tk.ProjectID == "" {
			tk.ProjectID = testProject
		}
		if err := s.CreateTask(ctx, tk); err != nil {
			t.Fatalf("seed task %q: %v", tk.ID, err)
		}
	}
}

func TestRegistry_PickReady_RespectsDeps(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	// dep is not yet done -> dependent must not be picked.
	seedTasks(t, s,
		statestore.Task{ID: "A-1", Status: "running"},
		statestore.Task{ID: "A-2", Status: "todo", Deps: []string{"A-1"}},
	)
	r := NewRegistry(s)

	if _, err := r.PickReady(ctx, testProject); !errors.Is(err, statestore.ErrNotFound) {
		t.Fatalf("dependent with undone dep should not be pickable, got err=%v", err)
	}

	// Mark the dep done; now the dependent becomes pickable.
	done := statestore.Task{ID: "A-1", ProjectID: testProject, Status: "done"}
	if err := s.UpdateTask(ctx, done); err != nil {
		t.Fatalf("mark dep done: %v", err)
	}
	got, err := r.PickReady(ctx, testProject)
	if err != nil {
		t.Fatalf("dependent should be pickable once dep is done: %v", err)
	}
	if got.ID != "A-2" {
		t.Fatalf("expected A-2, got %q", got.ID)
	}
}

func TestRegistry_PickReady_BlockedDepNeverReady(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	// A blocked dependency must gate the dependent so it is never pickable.
	seedTasks(t, s,
		statestore.Task{ID: "A-1", Status: "blocked"},
		statestore.Task{ID: "A-2", Status: "todo", Deps: []string{"A-1"}},
	)
	r := NewRegistry(s)

	if _, err := r.PickReady(ctx, testProject); !errors.Is(err, statestore.ErrNotFound) {
		t.Fatalf("task with blocked dep must never be pickable, got err=%v", err)
	}
}

func TestRegistry_PickReady_LowestOrderWins(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	// Several pickable tasks (no deps, statuses todo/ready) inserted out of order;
	// the lowest task ID must win deterministically regardless of insertion order.
	seedTasks(t, s,
		statestore.Task{ID: "C-9", Status: "ready"},
		statestore.Task{ID: "A-3", Status: "todo"},
		statestore.Task{ID: "B-7", Status: "ready"},
		statestore.Task{ID: "A-1", Status: "todo"},
	)
	r := NewRegistry(s)

	got, err := r.PickReady(ctx, testProject)
	if err != nil {
		t.Fatalf("PickReady: %v", err)
	}
	if got.ID != "A-1" {
		t.Fatalf("expected lowest-order task A-1, got %q", got.ID)
	}
}

func TestRegistry_PickReady_NoneReady_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	// Nothing pickable: one done, one running, one blocked.
	seedTasks(t, s,
		statestore.Task{ID: "A-1", Status: "done"},
		statestore.Task{ID: "A-2", Status: "running"},
		statestore.Task{ID: "A-3", Status: "blocked"},
	)
	r := NewRegistry(s)

	_, err := r.PickReady(ctx, testProject)
	if !errors.Is(err, statestore.ErrNotFound) {
		t.Fatalf("expected ErrNotFound when nothing is pickable, got %v", err)
	}
}

func TestRegistry_PickReady_LeaseHeldReturnsNone(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedTasks(t, s, statestore.Task{ID: "A-1", Status: "todo"})
	r := NewRegistry(s)

	// Hold the lease; even an otherwise-pickable task must not be returned.
	if err := r.AcquireLease(ctx, statestore.Lease{ProjectID: testProject, HostID: "h1", TaskID: "A-1", AcquiredAt: time.Unix(1, 0)}); err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	if _, err := r.PickReady(ctx, testProject); !errors.Is(err, statestore.ErrNotFound) {
		t.Fatalf("PickReady while leased must return ErrNotFound, got %v", err)
	}

	// After release the task is pickable again.
	if err := r.ReleaseLease(ctx, testProject); err != nil {
		t.Fatalf("release lease: %v", err)
	}
	if _, err := r.PickReady(ctx, testProject); err != nil {
		t.Fatalf("PickReady after release should succeed: %v", err)
	}
}

func TestRegistry_AcquireLease_SecondAcquireFails(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	r := NewRegistry(s)

	l := statestore.Lease{ProjectID: testProject, HostID: "h1", TaskID: "A-1", AcquiredAt: time.Unix(1, 0)}
	if err := r.AcquireLease(ctx, l); err != nil {
		t.Fatalf("first acquire should succeed: %v", err)
	}
	if err := r.AcquireLease(ctx, l); err == nil {
		t.Fatal("second acquire while leased must fail (repo-başına-1)")
	}

	// Release is idempotent: releasing twice (and an unleased project) is fine.
	if err := r.ReleaseLease(ctx, testProject); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := r.ReleaseLease(ctx, testProject); err != nil {
		t.Fatalf("idempotent release of unleased project must not error: %v", err)
	}
	if err := r.ReleaseLease(ctx, "never-leased"); err != nil {
		t.Fatalf("releasing an unleased project must not error: %v", err)
	}
}

func TestRegistry_Lifecycle_HappyPath(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedTasks(t, s, statestore.Task{ID: "A-1", Status: "todo"})
	r := NewRegistry(s)

	for _, to := range []string{"ready", "running", "done"} {
		got, err := r.Transition(ctx, "A-1", to)
		if err != nil {
			t.Fatalf("transition to %q: %v", to, err)
		}
		if got.Status != to {
			t.Fatalf("returned task status=%q, want %q", got.Status, to)
		}
		// Status must be read back through the store, never cached.
		persisted, err := s.GetTask(ctx, "A-1")
		if err != nil {
			t.Fatalf("get task: %v", err)
		}
		if persisted.Status != to {
			t.Fatalf("persisted status=%q, want %q", persisted.Status, to)
		}
	}
}

func TestRegistry_Lifecycle_IllegalTransitionRejected(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedTasks(t, s, statestore.Task{ID: "A-1", Status: "done"})
	r := NewRegistry(s)

	_, err := r.Transition(ctx, "A-1", "running")
	if err == nil {
		t.Fatal("done->running must be rejected")
	}
	if !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("expected ErrIllegalTransition, got %v", err)
	}
	// Must not persist the illegal transition.
	persisted, gerr := s.GetTask(ctx, "A-1")
	if gerr != nil {
		t.Fatalf("get task: %v", gerr)
	}
	if persisted.Status != "done" {
		t.Fatalf("illegal transition must not persist; status=%q", persisted.Status)
	}
}

func TestRegistry_Lifecycle_ToReadyGatedOnDeps(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedTasks(t, s,
		statestore.Task{ID: "A-1", Status: "blocked"},
		statestore.Task{ID: "A-2", Status: "todo", Deps: []string{"A-1"}},
	)
	r := NewRegistry(s)

	// A task whose dep is blocked must not be allowed to become ready.
	if _, err := r.Transition(ctx, "A-2", "ready"); err == nil {
		t.Fatal("todo->ready with a blocked dep must be rejected (dep-gate)")
	}
	persisted, err := s.GetTask(ctx, "A-2")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if persisted.Status != "todo" {
		t.Fatalf("dep-gated transition must not persist; status=%q", persisted.Status)
	}

	// Once the dep is done, the transition is allowed.
	if err := s.UpdateTask(ctx, statestore.Task{ID: "A-1", ProjectID: testProject, Status: "done"}); err != nil {
		t.Fatalf("mark dep done: %v", err)
	}
	if _, err := r.Transition(ctx, "A-2", "ready"); err != nil {
		t.Fatalf("todo->ready should succeed once dep is done: %v", err)
	}
}

// TestRegistry_PickReady_CapabilityRouting is the 2B-2 routing matrix: a task with
// Requires:[ios-build] is picked by a host whose capabilities are a superset, SKIPPED
// by an incapable host (which falls through to a different ready task / ErrNotFound),
// and picked by an UNCONSTRAINED host (no capabilities configured). A task with empty
// Requires is picked by all three (the empty set is a subset of any capability set).
func TestRegistry_PickReady_CapabilityRouting(t *testing.T) {
	ctx := context.Background()

	t.Run("capable host picks the iOS task", func(t *testing.T) {
		s := newStore(t)
		seedTasks(t, s, statestore.Task{ID: "A-1", Status: "todo", Requires: []string{"ios-build"}})
		r := NewRegistry(s, WithCapabilities([]string{"ios-build", "macos"}))

		got, err := r.PickReady(ctx, testProject)
		if err != nil {
			t.Fatalf("capable host should pick the iOS task: %v", err)
		}
		if got.ID != "A-1" {
			t.Fatalf("expected A-1, got %q", got.ID)
		}
	})

	t.Run("incapable host skips the iOS task (ErrNotFound when it is the only task)", func(t *testing.T) {
		s := newStore(t)
		seedTasks(t, s, statestore.Task{ID: "A-1", Status: "todo", Requires: []string{"ios-build"}})
		r := NewRegistry(s, WithCapabilities([]string{"linux"}))

		if _, err := r.PickReady(ctx, testProject); !errors.Is(err, statestore.ErrNotFound) {
			t.Fatalf("incapable host must skip the iOS task (ErrNotFound), got %v", err)
		}
	})

	t.Run("unconstrained host (no capabilities) picks the iOS task", func(t *testing.T) {
		s := newStore(t)
		seedTasks(t, s, statestore.Task{ID: "A-1", Status: "todo", Requires: []string{"ios-build"}})
		r := NewRegistry(s) // no WithCapabilities = unconstrained (pre-2B-2 behavior)

		got, err := r.PickReady(ctx, testProject)
		if err != nil {
			t.Fatalf("unconstrained host should pick the iOS task regardless of Requires: %v", err)
		}
		if got.ID != "A-1" {
			t.Fatalf("expected A-1, got %q", got.ID)
		}
	})

	t.Run("empty Requires is picked by all three hosts", func(t *testing.T) {
		for name, r := range map[string]func(statestore.StateStore) *Registry{
			"capable": func(s statestore.StateStore) *Registry {
				return NewRegistry(s, WithCapabilities([]string{"ios-build", "macos"}))
			},
			"incapable":     func(s statestore.StateStore) *Registry { return NewRegistry(s, WithCapabilities([]string{"linux"})) },
			"unconstrained": func(s statestore.StateStore) *Registry { return NewRegistry(s) },
		} {
			s := newStore(t)
			seedTasks(t, s, statestore.Task{ID: "A-1", Status: "todo"}) // empty Requires
			got, err := r(s).PickReady(ctx, testProject)
			if err != nil {
				t.Fatalf("[%s] empty-Requires task must be pickable by any host: %v", name, err)
			}
			if got.ID != "A-1" {
				t.Fatalf("[%s] expected A-1, got %q", name, got.ID)
			}
		}
	})

	t.Run("linux host skips the iOS task but picks a no-requires task (ordering preserved)", func(t *testing.T) {
		s := newStore(t)
		// A-1 needs iOS (skipped on linux); A-2 has no Requires and is pickable. Even
		// though A-1 sorts first, the linux host skips it and picks the lowest pickable.
		seedTasks(t, s,
			statestore.Task{ID: "A-1", Status: "todo", Requires: []string{"ios-build"}},
			statestore.Task{ID: "A-2", Status: "todo"},
		)
		r := NewRegistry(s, WithCapabilities([]string{"linux"}))

		got, err := r.PickReady(ctx, testProject)
		if err != nil {
			t.Fatalf("linux host should pick the no-requires task: %v", err)
		}
		if got.ID != "A-2" {
			t.Fatalf("expected A-2 (A-1 skipped for missing capability), got %q", got.ID)
		}
	})

	t.Run("multi-requires task needs the full subset", func(t *testing.T) {
		s := newStore(t)
		seedTasks(t, s, statestore.Task{ID: "A-1", Status: "todo", Requires: []string{"ios-build", "macos"}})
		// Has ios-build but NOT macos -> not a superset -> skipped.
		r := NewRegistry(s, WithCapabilities([]string{"ios-build", "linux"}))

		if _, err := r.PickReady(ctx, testProject); !errors.Is(err, statestore.ErrNotFound) {
			t.Fatalf("partial-capability host must skip a multi-requires task, got %v", err)
		}
	})

	t.Run("blank/empty WithCapabilities is treated as unconstrained", func(t *testing.T) {
		s := newStore(t)
		seedTasks(t, s, statestore.Task{ID: "A-1", Status: "todo", Requires: []string{"ios-build"}})
		r := NewRegistry(s, WithCapabilities([]string{"", "  "})) // all blank = unconstrained

		got, err := r.PickReady(ctx, testProject)
		if err != nil {
			t.Fatalf("blank capabilities must be unconstrained and pick the iOS task: %v", err)
		}
		if got.ID != "A-1" {
			t.Fatalf("expected A-1, got %q", got.ID)
		}
	})

	// L2: a stray-whitespace Requires entry must still route correctly — the host
	// set is trimmed at construction, so an un-normalized "docker " (trailing space)
	// or "" entry must be trimmed/dropped on the task side too, NOT become a silent
	// never-match.
	t.Run("stray-whitespace Requires entry still routes to a capable host", func(t *testing.T) {
		s := newStore(t)
		// "docker " (trailing space) matches a clean "docker" capability after trim.
		seedTasks(t, s, statestore.Task{ID: "A-1", Status: "todo", Requires: []string{"docker "}})
		r := NewRegistry(s, WithCapabilities([]string{"docker"}))

		got, err := r.PickReady(ctx, testProject)
		if err != nil {
			t.Fatalf("whitespace Requires must trim and match the capability, got %v", err)
		}
		if got.ID != "A-1" {
			t.Fatalf("expected A-1, got %q", got.ID)
		}
	})

	t.Run("blank Requires entry constrains nothing (dropped like host-side)", func(t *testing.T) {
		s := newStore(t)
		// A blank entry alongside a real one: the blank is dropped, only "docker" gates.
		seedTasks(t, s, statestore.Task{ID: "A-1", Status: "todo", Requires: []string{"", "docker", "  "}})
		r := NewRegistry(s, WithCapabilities([]string{"docker"}))

		got, err := r.PickReady(ctx, testProject)
		if err != nil {
			t.Fatalf("blank Requires entries must be dropped, not block routing: %v", err)
		}
		if got.ID != "A-1" {
			t.Fatalf("expected A-1, got %q", got.ID)
		}
	})
}

func TestRegistry_Transition_MissingTaskIsErrNotFound(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	r := NewRegistry(s)

	if _, err := r.Transition(ctx, "ghost", "ready"); !errors.Is(err, statestore.ErrNotFound) {
		t.Fatalf("transition on missing task must wrap ErrNotFound, got %v", err)
	}
}
