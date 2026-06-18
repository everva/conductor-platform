package conductor

import (
	"context"
	"errors"
	"fmt"

	"github.com/everva/conductor-platform/internal/statestore"
)

// Control reverse-channel APPROVE representation (governance N-10 human-hold /
// ADR-0003 risk-layered merge / ADR-0021 additive growth). This completes the
// operator vocabulary alongside pause/resume/abort for the ONE governance gap that
// had no operator action: a T3/T4 task whose develop+verify already PASSED but was
// HELD awaiting a human (handleHumanRequired) had no way to be APPROVED so its
// already-verified work could merge.
//
// Like abort, approve must be DURABLE in the SHARED StateStore because conductorctl
// (the operator client) and the conductor daemon are SEPARATE processes — an
// in-process flag is invisible to the daemon. It is a per-TASK signal: the additive
// Task.Approved flag (ADR-0021), set on the SPECIFIC held task.
//
// The semantics are the crux: approve does NOT re-develop. The held task's verified
// per-task BRANCH was preserved (Cleanup removes only the worktree, never the branch
// ref), and the conductor parked the task in StatusAwaitingApproval with that branch
// recorded on Task.Branch. RequestApprove just flips Approved=true on that exact
// task; a later daemon tick re-attaches the PRESERVED branch and merges it (after an
// optional cheap re-verify for base drift) — it never re-rolls develop, so the human
// approves the work they saw, not a fresh roll of the dice.
//
// `conductorctl approve --project <id> [--task <id>]` resolves WHICH task to approve:
// with --task it approves that task (validated to be awaiting-approval); without it,
// it auto-resolves the project's UNIQUE awaiting-approval task (the common case: one
// held task), erroring clearly when zero or many are held so the operator is never
// ambiguous about what they approved.
//
// StoreApprover is the ONE place that owns this representation: both conductorctl's
// operator-side RequestApprove and the daemon's approval resolver (Approver) go
// through it, so the two processes agree on shape — mirroring StoreAborter.

// StatusAwaitingApproval is the DISTINCT, durable task status a held T3/T4 task is
// parked in after a green gate (governance N-10): the work is VERIFIED and merge-
// ready but a human must approve it first. It is intentionally NOT "blocked" so it
// is unambiguously an awaiting-human state a UI surfaces as "onay bekliyor" rather
// than a generic failure, AND so registry PickReady (which only picks todo/ready)
// never re-develops it. The conductor writes it as a raw status string through the
// store (Status is an opaque string at the store seam, ADR-0010); it is an additive
// lifecycle value, not a frozen-signature change.
const StatusAwaitingApproval = "awaiting-approval"

// ErrNothingToApprove is returned by StoreApprover.RequestApprove when no task is
// resolvable for approval: either an explicit --task is not awaiting-approval, or
// (auto-resolve) the project has zero tasks awaiting approval. It is a comparable
// sentinel so the CLI can surface a clear, non-error-coded message.
var ErrNothingToApprove = errors.New("conductor: no task awaiting approval")

// ErrAmbiguousApproval is returned by RequestApprove when --task was omitted but the
// project has MORE THAN ONE task awaiting approval, so the operator must disambiguate
// with --task rather than the conductor guessing which held work to land.
var ErrAmbiguousApproval = errors.New("conductor: multiple tasks awaiting approval; specify --task")

// StoreApprover persists and reads the control reverse-channel APPROVE signal
// through a shared StateStore. It backs conductorctl's operator-side approve AND is
// read by the daemon's approval resolver (Approver), so an approval requested by one
// process is honored by the other when both point at the same store (e.g. the shared
// Postgres via -dsn). Construct with NewStoreApprover.
type StoreApprover struct {
	store statestore.StateStore
}

// NewStoreApprover returns a StoreApprover over the given shared StateStore.
func NewStoreApprover(store statestore.StateStore) *StoreApprover {
	return &StoreApprover{store: store}
}

// Compile-time assertion that *StoreApprover satisfies the daemon's approval seam.
var _ Approver = (*StoreApprover)(nil)

// RequestApprove marks a held task APPROVED so a later daemon tick merges its
// preserved verified branch (no re-develop). taskID may be empty to auto-resolve the
// project's UNIQUE awaiting-approval task; a non-empty taskID approves that specific
// task after validating it is awaiting-approval. It returns the approved task id (so
// the CLI reports exactly what it approved), ErrNothingToApprove when none is
// resolvable, and ErrAmbiguousApproval when taskID is empty but several are held.
func (a *StoreApprover) RequestApprove(ctx context.Context, projectID, taskID string) (string, error) {
	if taskID != "" {
		task, err := a.store.GetTask(ctx, taskID)
		if err != nil {
			return "", fmt.Errorf("conductor: approve %q: get task %q: %w", projectID, taskID, err)
		}
		if task.Status != StatusAwaitingApproval {
			return "", fmt.Errorf("conductor: approve task %q: status %q is not %q: %w",
				taskID, task.Status, StatusAwaitingApproval, ErrNothingToApprove)
		}
		if err := a.markApproved(ctx, task); err != nil {
			return "", err
		}
		return task.ID, nil
	}

	// Auto-resolve the project's awaiting-approval task(s).
	tasks, err := a.store.ListTasks(ctx, projectID)
	if err != nil {
		return "", fmt.Errorf("conductor: approve %q: list tasks: %w", projectID, err)
	}
	var held []statestore.Task
	for _, t := range tasks {
		if t.Status == StatusAwaitingApproval {
			held = append(held, t)
		}
	}
	switch len(held) {
	case 0:
		return "", fmt.Errorf("conductor: approve %q: %w", projectID, ErrNothingToApprove)
	case 1:
		if err := a.markApproved(ctx, held[0]); err != nil {
			return "", err
		}
		return held[0].ID, nil
	default:
		return "", fmt.Errorf("conductor: approve %q: %w", projectID, ErrAmbiguousApproval)
	}
}

// markApproved sets Approved=true on the task, re-reading first so it persists over
// the current store record. Idempotent: approving an already-approved task is a
// no-op rewrite.
func (a *StoreApprover) markApproved(ctx context.Context, task statestore.Task) error {
	cur, err := a.store.GetTask(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("conductor: approve: get task %q: %w", task.ID, err)
	}
	if cur.Approved {
		return nil
	}
	cur.Approved = true
	if err := a.store.UpdateTask(ctx, cur); err != nil {
		return fmt.Errorf("conductor: approve: update task %q: %w", task.ID, err)
	}
	return nil
}

// PendingApproval reports the project's APPROVED-and-held task ready to merge, if
// any, by reading the SHARED store (never a cached value). It is what the daemon's
// tick consults BEFORE PickReady so an approved held task is merged before any new
// develop starts. It returns the resolved task and true when exactly such a task
// exists; the zero task and false otherwise (no approved held task; a clean
// fall-through to normal picking). A store error is surfaced so a transient failure
// never silently drops an approval.
func (a *StoreApprover) PendingApproval(ctx context.Context, projectID string) (statestore.Task, bool, error) {
	tasks, err := a.store.ListTasks(ctx, projectID)
	if err != nil {
		return statestore.Task{}, false, fmt.Errorf("conductor: pending-approval %q: list tasks: %w", projectID, err)
	}
	for _, t := range tasks {
		if t.Status == StatusAwaitingApproval && t.Approved {
			return t, true, nil
		}
	}
	return statestore.Task{}, false, nil
}
