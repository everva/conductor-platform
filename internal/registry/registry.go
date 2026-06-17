// Package registry holds the platform's task-selection, lease, and lifecycle
// business logic on top of the frozen StateStore contract (ADR-0010 task-ledger,
// ADR-0008 repo-başına-1 lease, ADR-0004 task lifecycle).
//
// The Registry composes a statestore.StateStore and never reaches around it:
// there is no second backing map and no global singleton. Observed status is
// always read back through the store each call, because status is a
// derived/reconciled field and not authoritative truth about git
// (ADR-0010 §Güncelleme).
package registry

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/everva/conductor-platform/internal/statestore"
)

// Task lifecycle statuses (ADR-0004). They are the canonical values the registry
// writes on an explicit transition; the store treats Status as an opaque string.
const (
	// StatusTodo is the initial, unscheduled state.
	StatusTodo = "todo"
	// StatusReady marks a task whose dependencies are satisfied.
	StatusReady = "ready"
	// StatusRunning marks a task currently being executed.
	StatusRunning = "running"
	// StatusDone marks a task that has landed. It is terminal.
	StatusDone = "done"
	// StatusBlocked marks a task that failed and is awaiting retry/recovery.
	StatusBlocked = "blocked"
)

// ErrIllegalTransition is returned by Transition when the requested
// from→to status change is not part of the task lifecycle (ADR-0004). It is a
// comparable sentinel so callers can detect it with errors.Is.
var ErrIllegalTransition = errors.New("registry: illegal task transition")

// ErrDepsNotSatisfied is returned when a task cannot enter the ready state
// because one or more of its dependencies are not done (dep-gate, ADR-0004
// §Sonuç). A dependency that is blocked therefore keeps its dependents from ever
// becoming ready.
var ErrDepsNotSatisfied = errors.New("registry: dependencies not satisfied")

// legalTransitions maps each lifecycle status to the set of statuses it may
// transition to (ADR-0004). done is terminal; blocked re-enters ready on retry.
var legalTransitions = map[string][]string{
	StatusTodo:    {StatusReady},
	StatusReady:   {StatusRunning},
	StatusRunning: {StatusDone, StatusBlocked},
	StatusBlocked: {StatusReady},
	StatusDone:    {},
}

// Registry implements task selection, leasing, and lifecycle rules over a frozen
// StateStore. Construct it with NewRegistry and pass the store explicitly.
type Registry struct {
	store statestore.StateStore
}

// NewRegistry returns a Registry backed by the given StateStore. The store is the
// Registry's only state; the Registry adds no caching of its own.
func NewRegistry(store statestore.StateStore) *Registry {
	return &Registry{store: store}
}

// PickReady returns the single highest-priority pickable task for the project, or
// a wrapped statestore.ErrNotFound when none is pickable.
//
// A task is pickable when its status is todo or ready, every dependency resolves
// to a task with status done, and the project holds no active lease (ADR-0008).
// Among pickable tasks the lowest task ID wins, giving a deterministic, stable
// result regardless of the order ListTasks returns them in.
func (r *Registry) PickReady(ctx context.Context, projectID string) (statestore.Task, error) {
	// Lease-gate: a held lease means the repo is busy, so nothing is pickable.
	if _, err := r.store.GetLease(ctx, projectID); err == nil {
		return statestore.Task{}, fmt.Errorf("pick ready for project %q: lease held: %w", projectID, statestore.ErrNotFound)
	} else if !errors.Is(err, statestore.ErrNotFound) {
		return statestore.Task{}, fmt.Errorf("pick ready for project %q: check lease: %w", projectID, err)
	}

	tasks, err := r.store.ListTasks(ctx, projectID)
	if err != nil {
		return statestore.Task{}, fmt.Errorf("pick ready for project %q: list tasks: %w", projectID, err)
	}

	pickable := make([]statestore.Task, 0, len(tasks))
	for _, t := range tasks {
		if t.Status != StatusTodo && t.Status != StatusReady {
			continue
		}
		ok, derr := r.depsDone(ctx, t)
		if derr != nil {
			return statestore.Task{}, fmt.Errorf("pick ready for project %q: evaluate deps of %q: %w", projectID, t.ID, derr)
		}
		if ok {
			pickable = append(pickable, t)
		}
	}
	if len(pickable) == 0 {
		return statestore.Task{}, fmt.Errorf("pick ready for project %q: %w", projectID, statestore.ErrNotFound)
	}

	slices.SortFunc(pickable, func(a, b statestore.Task) int { return cmpString(a.ID, b.ID) })
	return pickable[0], nil
}

// AcquireLease takes the repo-scoped lease for the task via the store, enforcing
// one active lease per project: a second acquire while a lease is held returns an
// error (ADR-0008). It delegates to the store rather than tracking leases itself.
func (r *Registry) AcquireLease(ctx context.Context, l statestore.Lease) error {
	if err := r.store.AcquireLease(ctx, l); err != nil {
		return fmt.Errorf("acquire lease for project %q: %w", l.ProjectID, err)
	}
	return nil
}

// ReleaseLease releases the lease on the project via the store. It is idempotent:
// releasing a project that holds no lease is not an error.
func (r *Registry) ReleaseLease(ctx context.Context, projectID string) error {
	if err := r.store.ReleaseLease(ctx, projectID); err != nil {
		return fmt.Errorf("release lease for project %q: %w", projectID, err)
	}
	return nil
}

// Transition moves the task to the target status, persisting it through
// UpdateTask only when the change is legal (ADR-0004). The current status is read
// back from the store each call, never cached. An illegal transition returns
// ErrIllegalTransition and does not persist; a transition into ready additionally
// requires every dependency to be done (dep-gate) and otherwise returns
// ErrDepsNotSatisfied without persisting. A missing task wraps
// statestore.ErrNotFound.
func (r *Registry) Transition(ctx context.Context, taskID, to string) (statestore.Task, error) {
	current, err := r.store.GetTask(ctx, taskID)
	if err != nil {
		return statestore.Task{}, fmt.Errorf("transition task %q: %w", taskID, err)
	}

	if !transitionAllowed(current.Status, to) {
		return statestore.Task{}, fmt.Errorf("transition task %q from %q to %q: %w", taskID, current.Status, to, ErrIllegalTransition)
	}

	if to == StatusReady {
		ok, derr := r.depsDone(ctx, current)
		if derr != nil {
			return statestore.Task{}, fmt.Errorf("transition task %q to ready: evaluate deps: %w", taskID, derr)
		}
		if !ok {
			return statestore.Task{}, fmt.Errorf("transition task %q to ready: %w", taskID, ErrDepsNotSatisfied)
		}
	}

	current.Status = to
	if err := r.store.UpdateTask(ctx, current); err != nil {
		return statestore.Task{}, fmt.Errorf("transition task %q: persist: %w", taskID, err)
	}
	return current, nil
}

// depsDone reports whether every dependency of t resolves to a task with status
// done. A dependency that is missing, blocked, or otherwise not done yields
// false (it gates the dependent); a non-ErrNotFound store error is propagated.
func (r *Registry) depsDone(ctx context.Context, t statestore.Task) (bool, error) {
	for _, depID := range t.Deps {
		dep, err := r.store.GetTask(ctx, depID)
		if err != nil {
			if errors.Is(err, statestore.ErrNotFound) {
				return false, nil
			}
			return false, fmt.Errorf("get dependency %q: %w", depID, err)
		}
		if dep.Status != StatusDone {
			return false, nil
		}
	}
	return true, nil
}

// transitionAllowed reports whether moving from the current status to to is a
// legal lifecycle edge (ADR-0004).
func transitionAllowed(from, to string) bool {
	return slices.Contains(legalTransitions[from], to)
}

// cmpString orders strings ascending for a deterministic, stable pick.
func cmpString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
