package registry

import (
	"context"
	"testing"

	"github.com/everva/conductor-platform/internal/statestore"
)

// TestRegistry_PickReady_ApprovedHeldTaskIsMergeableFirst proves an approved held task is
// pickable AND outranks every develop candidate: it is finished, gate-verified work whose
// only remaining step is the squash-merge, so it lands before anything new starts.
func TestRegistry_PickReady_ApprovedHeldTaskIsMergeableFirst(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedTasks(t, s,
		statestore.Task{ID: "A-AUDIT-inventory", Status: StatusReady}, // remediation: normally first
		statestore.Task{ID: "B-01-feature", Status: StatusTodo},
		statestore.Task{ID: "Z-99-held", Status: StatusAwaitingApproval, Approved: true, Branch: "conductor/p/Z-99"},
	)

	got, err := NewRegistry(s).PickReady(ctx, testProject)
	if err != nil {
		t.Fatalf("PickReady: %v", err)
	}
	if got.ID != "Z-99-held" {
		t.Fatalf("approved held task must be picked first (mergeable-first), got %q", got.ID)
	}
	if got.Branch == "" {
		t.Fatalf("the preserved branch must ride along so the agent can merge it")
	}
}

// TestRegistry_PickReady_UnapprovedHeldTaskNotPicked pins the gate shut: a held task the
// director has NOT approved must never be handed to an agent — that is the whole point of
// holding it — and it must not block a genuinely ready task from being picked.
func TestRegistry_PickReady_UnapprovedHeldTaskNotPicked(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedTasks(t, s,
		statestore.Task{ID: "Z-99-held", Status: StatusAwaitingApproval, Approved: false, Branch: "conductor/p/Z-99"},
		statestore.Task{ID: "B-01-feature", Status: StatusTodo},
	)

	got, err := NewRegistry(s).PickReady(ctx, testProject)
	if err != nil {
		t.Fatalf("PickReady: %v", err)
	}
	if got.ID != "B-01-feature" {
		t.Fatalf("an UNapproved held task must not be picked, got %q", got.ID)
	}
}

// TestRegistry_PickReady_ApprovedHeldWithoutBranchNotPicked guards the merge contract: with no
// preserved branch there is nothing to merge, and handing the task back would make the agent
// re-develop verified work. It stays parked for a human instead.
func TestRegistry_PickReady_ApprovedHeldWithoutBranchNotPicked(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedTasks(t, s,
		statestore.Task{ID: "Z-99-held", Status: StatusAwaitingApproval, Approved: true, Branch: ""},
	)

	if _, err := NewRegistry(s).PickReady(ctx, testProject); err == nil {
		t.Fatalf("an approved held task with no branch must NOT be pickable")
	}
}

// TestRegistry_PickReady_TerminalStatusesStayUnpickable is a regression fence around isPickable:
// widening it for awaiting-approval must not have let any terminal state through.
func TestRegistry_PickReady_TerminalStatusesStayUnpickable(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	seedTasks(t, s,
		statestore.Task{ID: "T-done", Status: StatusDone, Approved: true, Branch: "b"},
		statestore.Task{ID: "T-cancelled", Status: StatusCancelled, Approved: true, Branch: "b"},
		statestore.Task{ID: "T-blocked", Status: StatusBlocked, Approved: true, Branch: "b"},
		statestore.Task{ID: "T-running", Status: StatusRunning, Approved: true, Branch: "b"},
	)

	if got, err := NewRegistry(s).PickReady(ctx, testProject); err == nil {
		t.Fatalf("no terminal/in-flight status may be pickable, got %q (%s)", got.ID, got.Status)
	}
}
