package conductor

import (
	"context"
	"errors"
	"fmt"

	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/statestore"
)

// Control reverse-channel ABORT representation (ADR-0011 §4, ADR-0020 follow-up,
// ADR-0021 additive growth). This completes the pause/resume/abort control trio.
//
// Like pause, abort must be DURABLE in the SHARED StateStore because conductorctl
// (the operator client) and the conductor daemon are SEPARATE processes — an
// in-process flag is invisible to the daemon. Unlike pause (a project-level
// run-state that gates STARTING new work), abort targets the ONE task currently
// RUNNING and tells the daemon to CANCEL its in-flight develop mid-flight and
// revert it to a safe state (NO verify, NO merge). It is therefore a per-TASK
// signal: the additive Task.AbortRequested flag (ADR-0021).
//
// `conductorctl abort --project <id>` does not know the running task id, so it
// RESOLVES it through the project's active lease: the conductor leases the repo
// for the task it is running (Lease.TaskID), so the lease is the authoritative
// "what is running right now" record. No lease == nothing is running == nothing to
// abort (a clear ErrNothingRunning, not a silent success).
//
// StoreAborter is the ONE place that owns this representation: both the daemon's
// running-develop watcher (which reads AbortRequested off the leased task) and
// conductorctl's operator-side RequestAbort go through it, so the two processes
// agree on shape — mirroring StorePauser.

// ErrNothingRunning is returned by StoreAborter.RequestAbort when the project
// holds no lease, i.e. no task is currently running, so there is nothing to abort.
// It is a comparable sentinel so the CLI can surface a clear "nothing to abort"
// message (and a distinct exit) rather than a generic failure.
var ErrNothingRunning = errors.New("conductor: no task running to abort")

// StoreAborter persists and reads the control reverse-channel ABORT signal through
// a shared StateStore. It backs conductorctl's operator-side abort AND is read by
// the daemon's running-develop watcher (Aborter), so an abort requested by one
// process is honored by the other when both point at the same store (e.g. the
// shared Postgres via -dsn). Construct with NewStoreAborter.
type StoreAborter struct {
	store statestore.StateStore
}

// NewStoreAborter returns a StoreAborter over the given shared StateStore.
func NewStoreAborter(store statestore.StateStore) *StoreAborter {
	return &StoreAborter{store: store}
}

// Compile-time assertion that *StoreAborter satisfies the daemon's abort-watch seam.
var _ Aborter = (*StoreAborter)(nil)

// RequestAbort resolves the project's currently-running task via its active lease
// and persists the durable ABORT signal (engine.AbortCommand) onto that task so the
// daemon's watcher cancels its in-flight develop. It returns the resolved task id
// (so the CLI can report exactly what it aborted) and ErrNothingRunning when the
// project holds no lease (nothing to abort). An unknown lease task surfaces a clear
// wrapped error rather than a silent no-op.
func (a *StoreAborter) RequestAbort(ctx context.Context, projectID string) (taskID string, err error) {
	lease, err := a.store.GetLease(ctx, projectID)
	if err != nil {
		if errors.Is(err, statestore.ErrNotFound) {
			return "", fmt.Errorf("conductor: abort %q: %w", projectID, ErrNothingRunning)
		}
		return "", fmt.Errorf("conductor: abort %q: get lease: %w", projectID, err)
	}
	if lease.TaskID == "" {
		// A lease with no task id cannot identify a running task to abort.
		return "", fmt.Errorf("conductor: abort %q: %w", projectID, ErrNothingRunning)
	}
	if err := a.apply(ctx, lease.TaskID, engine.AbortCommand()); err != nil {
		return "", err
	}
	return lease.TaskID, nil
}

// AbortRequested reports whether taskID has a pending ABORT signal by reading the
// task's flag off the shared store (never a cached value). It is what the daemon's
// running-develop watcher polls on the leased task. An unknown task is reported as
// not-aborting with a wrapped error so a transient store/lease race never crashes
// the watcher into cancelling blindly.
func (a *StoreAborter) AbortRequested(ctx context.Context, taskID string) (bool, error) {
	task, err := a.store.GetTask(ctx, taskID)
	if err != nil {
		return false, fmt.Errorf("conductor: abort-requested: get task %q: %w", taskID, err)
	}
	return task.AbortRequested, nil
}

// ClearAbort clears the ABORT signal on taskID so a subsequent re-run of the task
// is not immediately re-aborted by a stale flag. It is invoked by the conductor as
// part of handling an abort (after the develop is cancelled and the task reverted),
// keeping the signal one-shot. Idempotent: clearing an already-clear task is a
// no-op rewrite.
func (a *StoreAborter) ClearAbort(ctx context.Context, taskID string) error {
	task, err := a.store.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("conductor: clear abort: get task %q: %w", taskID, err)
	}
	if !task.AbortRequested {
		return nil
	}
	task.AbortRequested = false
	if err := a.store.UpdateTask(ctx, task); err != nil {
		return fmt.Errorf("conductor: clear abort: update task %q: %w", taskID, err)
	}
	return nil
}

// apply translates a typed control Command into the task's abort signal and
// persists it via UpdateTask. Only abort touches the flag here; an unsupported verb
// is rejected rather than silently ignored, keeping the reverse-channel honest
// (pause/resume are a project-level run-state owned by StorePauser, not a task flag).
func (a *StoreAborter) apply(ctx context.Context, taskID string, cmd engine.Command) error {
	if cmd.Action != engine.ActionAbort {
		return fmt.Errorf("conductor: control: task %q: unsupported task-signal action %q", taskID, cmd.Action)
	}
	task, err := a.store.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("conductor: control abort: get task %q: %w", taskID, err)
	}
	if task.AbortRequested {
		return nil // idempotent: already flagged for abort.
	}
	task.AbortRequested = true
	if err := a.store.UpdateTask(ctx, task); err != nil {
		return fmt.Errorf("conductor: control abort: update task %q: %w", taskID, err)
	}
	return nil
}
