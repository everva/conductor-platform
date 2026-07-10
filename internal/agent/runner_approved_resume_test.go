package agent

import (
	"context"
	"testing"

	"github.com/everva/conductor-platform/internal/agentclient"
)

func heldApproved() *agentclient.LeasedTask {
	return &agentclient.LeasedTask{Task: agentclient.TaskInfo{
		ID: "T-1", ProjectID: "p", Tier: "T2",
		Status: "awaiting-approval", Approved: true, Branch: "conductor/p/T-1",
	}}
}

// TestRunOnce_ApprovedHeldTask_MergesWithoutDeveloping proves an approval survives the agent
// process that was polling for it: a NEW agent that leases the approved held task merges the
// PRESERVED branch and never re-develops (a re-develop would cut a fresh branch from the base
// and silently throw away the gate-verified commit the director approved).
func TestRunOnce_ApprovedHeldTask_MergesWithoutDeveloping(t *testing.T) {
	gw := &fakeGateway{leaseTask: heldApproved()}
	ex := &fakeExecutor{mergeSHA: "9f2c1ab"}

	out, err := newRunner(gw, ex).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if out != OutcomeMerged {
		t.Fatalf("outcome = %q, want %q", out, OutcomeMerged)
	}
	if ex.runs != 0 {
		t.Fatalf("the approved branch must NOT be re-developed; Run called %d time(s)", ex.runs)
	}
	if ex.merges != 1 {
		t.Fatalf("merges = %d, want 1", ex.merges)
	}
	if !ex.mergeApproved {
		t.Fatalf("Merge must be called with approved=true so it re-verifies against the current base")
	}
	if gw.mergedSHA != "9f2c1ab" {
		t.Fatalf("merged sha = %q, want 9f2c1ab", gw.mergedSHA)
	}
	if !gw.released {
		t.Fatalf("the lease must always be released")
	}
}

// TestRunOnce_HeldButNotApproved_Develops pins the resume path shut for a task the director has
// not approved: reaching RunOnce with such a task (defence in depth — PickReady already filters
// it) must take the ordinary develop path, never a merge.
func TestRunOnce_HeldButNotApproved_Develops(t *testing.T) {
	lt := heldApproved()
	lt.Task.Approved = false
	gw := &fakeGateway{leaseTask: lt, resultDecision: "blocked"}
	ex := &fakeExecutor{out: RunOutcome{Result: "blocked", Summary: "gate"}}

	if _, err := newRunner(gw, ex).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if ex.merges != 0 {
		t.Fatalf("an UNapproved held task must not be merged")
	}
	if ex.runs != 1 {
		t.Fatalf("an UNapproved task takes the develop path; Run called %d time(s)", ex.runs)
	}
}

// TestRunOnce_ApprovedButNoBranch_Develops guards the merge contract: with no preserved branch
// there is nothing to merge, so the agent must not call Merge with an empty branch.
func TestRunOnce_ApprovedButNoBranch_Develops(t *testing.T) {
	lt := heldApproved()
	lt.Task.Branch = ""
	gw := &fakeGateway{leaseTask: lt, resultDecision: "blocked"}
	ex := &fakeExecutor{out: RunOutcome{Result: "blocked", Summary: "gate"}}

	if _, err := newRunner(gw, ex).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if ex.merges != 0 {
		t.Fatalf("must not merge an approved task with no preserved branch")
	}
}
