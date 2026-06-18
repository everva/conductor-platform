package conductor

import (
	"context"
	"fmt"

	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/statestore"
)

// Control reverse-channel pause representation (ADR-0011 §4, ADR-0021).
//
// The crux of the reverse-channel is that conductorctl (the operator client) and
// the conductor daemon are SEPARATE processes, so a pause must be DURABLE in the
// SHARED StateStore — an in-process flag is invisible to the daemon. ADR-0021
// relaxed the frozen StateStore to allow ADDITIVE growth, so pause is now a
// FIRST-CLASS, observable field on the Project (Project.Paused), read/written
// atomically via GetProject/UpdateProject. This retires the ADR-0020 marker-task
// workaround (a reserved `__conductor.paused__:<id>` Task): pause is no longer a
// task, so it never appears in ListTasks/intake/PickReady and needs no hiding.
//
// StorePauser is the ONE place that owns this representation: both the daemon's
// pause-gate (conductor.Pauser) and conductorctl's operator-side Pause/Resume go
// through it, so the two processes agree on shape.

// StorePauser persists and reads the control reverse-channel pause state through
// a shared StateStore. It satisfies conductor.Pauser (Paused) for the daemon's
// tick AND backs conductorctl's operator-side Pause/Resume, so a pause set by one
// process is honored by the other when both point at the same store (e.g. the
// shared Postgres via -dsn). Construct with NewStorePauser.
type StorePauser struct {
	store statestore.StateStore
}

// NewStorePauser returns a StorePauser over the given shared StateStore.
func NewStorePauser(store statestore.StateStore) *StorePauser {
	return &StorePauser{store: store}
}

// Compile-time assertion that *StorePauser satisfies the daemon's pause-gate seam.
var _ Pauser = (*StorePauser)(nil)

// Paused reports whether projectID is paused by reading the project's first-class
// run-state off the shared store (never a cached flag). An unknown project is
// reported as not paused (no project, no run-state to honor).
func (p *StorePauser) Paused(ctx context.Context, projectID string) (bool, error) {
	proj, err := p.store.GetProject(ctx, projectID)
	if err != nil {
		return false, fmt.Errorf("conductor: paused: get project %q: %w", projectID, err)
	}
	return proj.Paused, nil
}

// Pause persists the pause directive (engine.PauseCommand) onto the project's
// run-state. Idempotent: pausing an already-paused project re-writes Paused=true.
// An unknown project surfaces a clear (wrapped ErrNotFound) error.
func (p *StorePauser) Pause(ctx context.Context, projectID string) error {
	return p.apply(ctx, projectID, engine.PauseCommand())
}

// Resume persists the resume directive (engine.ResumeCommand), clearing the
// project's pause run-state so ticks proceed. Idempotent.
func (p *StorePauser) Resume(ctx context.Context, projectID string) error {
	return p.apply(ctx, projectID, engine.ResumeCommand())
}

// apply translates a typed control Command into the project's pause run-state and
// persists it atomically via UpdateProject. Only pause/resume touch run-state
// here; an unsupported verb (e.g. abort) is rejected rather than silently ignored,
// keeping the reverse-channel honest (ABORT is part of the frozen vocabulary but
// is a documented follow-up, not a run-state toggle).
func (p *StorePauser) apply(ctx context.Context, projectID string, cmd engine.Command) error {
	var paused bool
	switch cmd.Action {
	case engine.ActionPause:
		paused = true
	case engine.ActionResume:
		paused = false
	default:
		return fmt.Errorf("conductor: control: project %q: unsupported run-state action %q", projectID, cmd.Action)
	}

	proj, err := p.store.GetProject(ctx, projectID)
	if err != nil {
		return fmt.Errorf("conductor: control %q: get project %q: %w", cmd.Action, projectID, err)
	}
	if proj.Paused == paused {
		return nil // idempotent: already in the desired run-state.
	}
	proj.Paused = paused
	if err := p.store.UpdateProject(ctx, proj); err != nil {
		return fmt.Errorf("conductor: control %q: update project %q: %w", cmd.Action, projectID, err)
	}
	return nil
}
