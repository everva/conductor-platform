package conductor

import (
	"context"
	"errors"
	"fmt"

	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/statestore"
)

// Control reverse-channel pause representation (ADR-0011 §4).
//
// The crux of the reverse-channel is that conductorctl (the operator client) and
// the conductor daemon are SEPARATE processes, so a pause must be DURABLE in the
// SHARED StateStore — an in-process flag is invisible to the daemon. The FROZEN
// StateStore exposes NO project mutation (CreateProject is ON CONFLICT DO
// NOTHING; there is no UpdateProject), and a lease would be TTL-reaped and
// counted by the resource governor. The one durable, project-scoped, reversible,
// non-lease record the frozen contract CAN mutate is a Task (UpdateTask). So the
// pause rides a dedicated, reserved MARKER task per project whose Status carries
// the paused flag.
//
// The marker is invisible to the real ledger: its ProjectID is left EMPTY, so it
// never appears in ListTasks(<realProject>) (status rendering, intake, and
// PickReady all scope by project), and its reserved ID prefix cannot collide with
// an operator-authored task or scenario id (those are upper-alnum/hyphen like A-1
// / PRE-0; the marker prefix uses lowercase + dots + a colon). The id is also
// NUL-free so it is a legal Postgres `text` primary key. It is fetched directly by
// id via GetTask. StorePauser is the ONE
// place that knows this shape: both the daemon's pause-gate (conductor.Pauser)
// and conductorctl's operator-side Pause/Resume go through it, so the two
// processes agree on representation.
const (
	// markerStatusPaused is the marker task's Status when the project is paused.
	markerStatusPaused = "paused"
	// markerStatusRunning is the marker task's Status when the project is running.
	markerStatusRunning = "running"
)

// markerIDPrefix is the reserved namespace for pause-marker task ids. It uses
// lowercase + dots + a colon so it cannot collide with an operator-authored task
// or scenario id (those are upper-alnum/hyphen like A-1 / PRE-0), and it is
// NUL-free so it is a legal Postgres `text` primary key.
const markerIDPrefix = "__conductor.paused__:"

// pauseMarkerID returns the reserved, collision-proof marker-task id for a
// project.
func pauseMarkerID(projectID string) string {
	return markerIDPrefix + projectID
}

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

// Paused reports whether projectID is paused by reading the durable marker off
// the shared store (never a cached flag). A missing marker means never paused.
func (p *StorePauser) Paused(ctx context.Context, projectID string) (bool, error) {
	t, err := p.store.GetTask(ctx, pauseMarkerID(projectID))
	if errors.Is(err, statestore.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("conductor: paused: get marker for %q: %w", projectID, err)
	}
	return t.Status == markerStatusPaused, nil
}

// Pause persists the pause directive (engine.PauseCommand) onto the project's
// durable marker. Idempotent: pausing an already-paused project re-writes the
// same paused status.
func (p *StorePauser) Pause(ctx context.Context, projectID string) error {
	return p.apply(ctx, projectID, engine.PauseCommand())
}

// Resume persists the resume directive (engine.ResumeCommand), flipping the
// marker back to running so ticks proceed. Idempotent.
func (p *StorePauser) Resume(ctx context.Context, projectID string) error {
	return p.apply(ctx, projectID, engine.ResumeCommand())
}

// apply translates a typed control Command into the durable marker status and
// upserts it through the shared store: a first pause CREATEs the marker, later
// toggles UPDATE it. Only pause/resume touch run-state here; an unsupported verb
// (e.g. abort) is rejected rather than silently ignored, keeping the
// reverse-channel honest (ABORT is part of the frozen vocabulary but is a
// documented follow-up, not a run-state toggle).
func (p *StorePauser) apply(ctx context.Context, projectID string, cmd engine.Command) error {
	var status string
	switch cmd.Action {
	case engine.ActionPause:
		status = markerStatusPaused
	case engine.ActionResume:
		status = markerStatusRunning
	default:
		return fmt.Errorf("conductor: control: project %q: unsupported run-state action %q", projectID, cmd.Action)
	}

	id := pauseMarkerID(projectID)
	marker, err := p.store.GetTask(ctx, id)
	switch {
	case errors.Is(err, statestore.ErrNotFound):
		// First control directive for this project: create the marker. ProjectID is
		// left empty so the marker never surfaces in the real project's ledger.
		if cerr := p.store.CreateTask(ctx, statestore.Task{ID: id, Status: status}); cerr != nil {
			return fmt.Errorf("conductor: control %q: create marker for %q: %w", cmd.Action, projectID, cerr)
		}
		return nil
	case err != nil:
		return fmt.Errorf("conductor: control %q: get marker for %q: %w", cmd.Action, projectID, err)
	default:
		marker.Status = status
		if uerr := p.store.UpdateTask(ctx, marker); uerr != nil {
			return fmt.Errorf("conductor: control %q: update marker for %q: %w", cmd.Action, projectID, uerr)
		}
		return nil
	}
}
