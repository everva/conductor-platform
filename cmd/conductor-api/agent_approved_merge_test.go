package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/everva/conductor-platform/internal/statestore"
)

// TestAgentReleaseLease_NoLease_RevertsRunningToReady is the phantom-task fence.
//
// The reconcile reaper measures a lease's TTL from AcquiredAt — the age of the WORK, not of any
// liveness signal — so a develop that outlives the TTL (agents run with -timeout 45m/60m against
// a 30m default) has its lease reaped while the agent is perfectly healthy. When that agent then
// releases without a terminal verdict, it is no longer the "owner", and before this fix the
// running→ready revert was skipped: the task stayed "running" forever, held by nobody, and no
// API could recover it (/retry takes only blocked, /abort needs a lease).
func TestAgentReleaseLease_NoLease_RevertsRunningToReady(t *testing.T) {
	s, store := agentServer(t)
	ctx := context.Background()
	seedProject(t, store, "p")
	seedTodoTask(t, store, "p", "T-1", nil)

	if rec := doBody(t, s, http.MethodPost, "/projects/p/agent/lease", bearer(), `{"host_id":"davinci","capabilities":["backend"]}`); rec.Code != http.StatusOK {
		t.Fatalf("lease status = %d, want 200", rec.Code)
	}
	// Simulate the reaper freeing the lease under the still-running agent.
	if err := store.ReleaseLease(ctx, "p"); err != nil {
		t.Fatalf("simulate reap: %v", err)
	}
	if got, _ := store.GetTask(ctx, "T-1"); got.Status != "running" {
		t.Fatalf("precondition: task should still be running, got %q", got.Status)
	}

	if rec := doBody(t, s, http.MethodPost, "/projects/p/agent/lease/release", bearer(), `{"host_id":"davinci","task_id":"T-1"}`); rec.Code != http.StatusOK {
		t.Fatalf("release status = %d, want 200", rec.Code)
	}
	got, err := store.GetTask(ctx, "T-1")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.Status != "ready" {
		t.Fatalf("a release with NO lease present must revert running→ready (not strand it), got %q", got.Status)
	}
}

// TestAgentLease_ApprovedHeldTask_KeepsStatusAndCarriesApproval proves the lease of an approved
// held task hands the agent everything it needs to MERGE rather than develop: the status stays
// awaiting-approval (it is not clobbered to "running", which would erase the only signal that
// distinguishes verified work from work still to be built), and approved+branch ride along.
func TestAgentLease_ApprovedHeldTask_KeepsStatusAndCarriesApproval(t *testing.T) {
	s, store := agentServer(t)
	ctx := context.Background()
	seedProject(t, store, "p")
	if err := store.CreateTask(ctx, statestore.Task{
		ID: "T-9", ProjectID: "p", Lane: "backend", Tier: "T3",
		Status: "awaiting-approval", Approved: true, Branch: "conductor/p/T-9",
	}); err != nil {
		t.Fatalf("seed held task: %v", err)
	}

	rec := doBody(t, s, http.MethodPost, "/projects/p/agent/lease", bearer(), `{"host_id":"davinci","capabilities":["backend"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("lease status = %d, want 200 (an approved held task must be leasable)", rec.Code)
	}
	var body struct {
		Task struct {
			ID       string `json:"id"`
			Status   string `json:"status"`
			Approved bool   `json:"approved"`
			Branch   string `json:"branch"`
		} `json:"task"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode lease response: %v", err)
	}
	if body.Task.ID != "T-9" {
		t.Fatalf("leased task = %q, want T-9", body.Task.ID)
	}
	if body.Task.Status != "awaiting-approval" {
		t.Fatalf("status must stay awaiting-approval, got %q", body.Task.Status)
	}
	if !body.Task.Approved || body.Task.Branch != "conductor/p/T-9" {
		t.Fatalf("approved/branch must ride the lease: approved=%v branch=%q", body.Task.Approved, body.Task.Branch)
	}
	// And the STORE must not have been mutated to running either.
	if got, _ := store.GetTask(ctx, "T-9"); got.Status != "awaiting-approval" {
		t.Fatalf("stored status = %q, want awaiting-approval", got.Status)
	}
}
