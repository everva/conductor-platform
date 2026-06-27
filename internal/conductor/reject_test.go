package conductor

import (
	"context"
	"errors"
	"testing"

	"github.com/everva/conductor-platform/internal/registry"
	"github.com/everva/conductor-platform/internal/statestore"
)

// TestStoreRejecter_Resolution mirrors TestStoreApprover_Resolution: reject resolves WHICH held task
// identically to approve (reusing the same sentinels), moves it to the terminal StatusRejected with
// a reason, is idempotent, and leaves it INERT — never surfaced to the approver to merge.
func TestStoreRejecter_Resolution(t *testing.T) {
	ctx := context.Background()
	store := statestore.NewMemoryStore()
	if err := store.CreateProject(ctx, statestore.Project{ID: projectID, BaseBranch: "develop"}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	rj := NewStoreRejecter(store)

	// Nothing held -> ErrNothingToApprove (the reused sentinel).
	if _, err := rj.RequestReject(ctx, projectID, "", ""); !errors.Is(err, ErrNothingToApprove) {
		t.Fatalf("empty project: err = %v, want ErrNothingToApprove", err)
	}

	// A ready (not-held) task is not rejectable by explicit id.
	if err := store.CreateTask(ctx, statestore.Task{ID: "R-1", ProjectID: projectID, Status: registry.StatusReady}); err != nil {
		t.Fatalf("seed ready task: %v", err)
	}
	if _, err := rj.RequestReject(ctx, projectID, "R-1", ""); !errors.Is(err, ErrNothingToApprove) {
		t.Fatalf("ready task: err = %v, want ErrNothingToApprove", err)
	}

	// Two held tasks -> ambiguity without an explicit task.
	for _, id := range []string{"H-1", "H-2"} {
		if err := store.CreateTask(ctx, statestore.Task{ID: id, ProjectID: projectID, Status: StatusAwaitingApproval, Branch: "conductor/" + id}); err != nil {
			t.Fatalf("seed held %s: %v", id, err)
		}
	}
	if _, err := rj.RequestReject(ctx, projectID, "", ""); !errors.Is(err, ErrAmbiguousApproval) {
		t.Fatalf("two held: err = %v, want ErrAmbiguousApproval", err)
	}

	// Explicit task disambiguates and moves it to the terminal rejected status with a reason.
	id, err := rj.RequestReject(ctx, projectID, "H-2", "not what I wanted")
	if err != nil {
		t.Fatalf("explicit reject: %v", err)
	}
	if id != "H-2" {
		t.Fatalf("rejected id = %q, want H-2", id)
	}
	got, _ := store.GetTask(ctx, "H-2")
	if got.Status != StatusRejected {
		t.Fatalf("H-2 status = %q, want %q", got.Status, StatusRejected)
	}
	if got.LastError != "not what I wanted" {
		t.Fatalf("rejected task reason = %q, want the supplied reason", got.LastError)
	}
	if got.Approved {
		t.Fatalf("a rejected task must NOT be approved")
	}

	// Idempotent: re-rejecting the same task is a no-op (no error, still rejected).
	if _, err := rj.RequestReject(ctx, projectID, "H-2", "again"); err != nil {
		t.Fatalf("re-reject: %v", err)
	}

	// A blank reason defaults to a clear message (so the board never shows an empty reason).
	if _, err := rj.RequestReject(ctx, projectID, "H-1", ""); err != nil {
		t.Fatalf("reject H-1: %v", err)
	}
	h1, _ := store.GetTask(ctx, "H-1")
	if h1.LastError == "" {
		t.Fatalf("a rejected task must carry a default reason when none is supplied")
	}

	// INERT: with both held tasks rejected, the approver surfaces NOTHING to merge — a rejected task
	// never becomes pending-approval (registry.PickReady's todo/ready-only filter, proven in the
	// registry suite, is what keeps it out of the develop path).
	if _, ok, err := NewStoreApprover(store).PendingApproval(ctx, projectID); err != nil || ok {
		t.Fatalf("PendingApproval over rejected tasks = (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}
