package conductor

import (
	"context"
	"fmt"

	"github.com/everva/conductor-platform/internal/statestore"
)

// Held-task REJECT (additive, ADR-0021) — the counterpart to approve. A T3/T4 task whose
// develop+verify PASSED is parked in StatusAwaitingApproval awaiting a human; until now the only
// operator action was approve (merge). A director who does NOT want that work had a dead-end. Reject
// moves the held task to the terminal StatusRejected so it leaves the review queue WITHOUT merging.
//
// SAFE-by-construction (no daemon change, no migration — Status is an opaque string at the store
// seam, ADR-0010): "rejected" is NEITHER todo/ready (so registry.PickReady never re-develops it)
// NOR (awaiting-approval && Approved) (so StoreApprover.PendingApproval never merges it), and its
// branch is never merged so reconcile (which only marks tasks present in a base-branch [task:<id>]
// trailer done) never touches it. The preserved per-task branch is left in the repo — the director
// declined the work; pruning the branch is a manual/ops choice, not a silent drop.

// StatusRejected is the terminal status a held task is moved to when the director rejects the merge.
const StatusRejected = "rejected"

// StoreRejecter rejects a held awaiting-approval task through the shared StateStore, mirroring
// StoreApprover. It resolves WHICH held task identically to approve (by task id, or the project's
// UNIQUE awaiting-approval task) so the operator is never ambiguous about what they rejected — it
// REUSES ErrNothingToApprove / ErrAmbiguousApproval so the gateway maps approve and reject the same
// way — then moves it to StatusRejected with a short reason on Task.LastError.
type StoreRejecter struct {
	store statestore.StateStore
}

// NewStoreRejecter returns a StoreRejecter over the given shared StateStore.
func NewStoreRejecter(store statestore.StateStore) *StoreRejecter {
	return &StoreRejecter{store: store}
}

// RequestReject moves a held task to StatusRejected (no merge, no re-develop). taskID may be empty
// to auto-resolve the project's UNIQUE awaiting-approval task; a non-empty taskID rejects that
// specific task after validating it is awaiting-approval. reason (optional) is recorded on
// Task.LastError so the board can show WHY it left the queue. Returns the rejected task id,
// ErrNothingToApprove when none is resolvable, and ErrAmbiguousApproval when several are held.
func (r *StoreRejecter) RequestReject(ctx context.Context, projectID, taskID, reason string) (string, error) {
	held, err := r.resolveHeld(ctx, projectID, taskID)
	if err != nil {
		return "", err
	}
	// Re-read so the write persists over the current store record (mirrors markApproved).
	cur, err := r.store.GetTask(ctx, held.ID)
	if err != nil {
		return "", fmt.Errorf("conductor: reject: get task %q: %w", held.ID, err)
	}
	if cur.Status == StatusRejected {
		return cur.ID, nil // idempotent
	}
	cur.Status = StatusRejected
	if reason == "" {
		reason = "rejected by director"
	}
	cur.LastError = reason
	if err := r.store.UpdateTask(ctx, cur); err != nil {
		return "", fmt.Errorf("conductor: reject: update task %q: %w", held.ID, err)
	}
	return cur.ID, nil
}

// resolveHeld picks the held task to reject: the explicit taskID (validated awaiting-approval), or
// the project's UNIQUE awaiting-approval task — erroring identically to approve so the operator is
// never ambiguous. Kept separate from StoreApprover so the frozen approve path is untouched.
func (r *StoreRejecter) resolveHeld(ctx context.Context, projectID, taskID string) (statestore.Task, error) {
	if taskID != "" {
		task, err := r.store.GetTask(ctx, taskID)
		if err != nil {
			return statestore.Task{}, fmt.Errorf("conductor: reject %q: get task %q: %w", projectID, taskID, err)
		}
		// Held → rejectable; ALREADY rejected → accepted so an explicit re-reject is an idempotent
		// no-op (RequestReject short-circuits on StatusRejected). Any other status is not rejectable.
		if task.Status != StatusAwaitingApproval && task.Status != StatusRejected {
			return statestore.Task{}, fmt.Errorf("conductor: reject task %q: status %q is not %q: %w",
				taskID, task.Status, StatusAwaitingApproval, ErrNothingToApprove)
		}
		return task, nil
	}
	tasks, err := r.store.ListTasks(ctx, projectID)
	if err != nil {
		return statestore.Task{}, fmt.Errorf("conductor: reject %q: list tasks: %w", projectID, err)
	}
	var held []statestore.Task
	for _, t := range tasks {
		if t.Status == StatusAwaitingApproval {
			held = append(held, t)
		}
	}
	switch len(held) {
	case 0:
		return statestore.Task{}, fmt.Errorf("conductor: reject %q: %w", projectID, ErrNothingToApprove)
	case 1:
		return held[0], nil
	default:
		return statestore.Task{}, fmt.Errorf("conductor: reject %q: %w", projectID, ErrAmbiguousApproval)
	}
}
