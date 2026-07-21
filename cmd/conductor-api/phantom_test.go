package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/everva/conductor-platform/internal/statestore"
)

// A project leases per PROJECT, so exactly one of its tasks can legitimately be running. These
// tests pin the consequence: a task left in "running" WITHOUT the lease is a phantom, and the
// lease poll frees it — while the genuinely leased task is never touched.

func phantomFixture(t *testing.T) (*apiServer, statestore.StateStore, context.Context) {
	t.Helper()
	store := statestore.NewMemoryStore()
	s := &apiServer{store: store, token: testToken, clock: fixedClock}
	ctx := context.Background()
	if err := store.CreateProject(ctx, statestore.Project{ID: "p1", Repo: "git@x/p1.git", BaseBranch: "develop", Readiness: "ready"}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	return s, store, ctx
}

func mustTask(t *testing.T, store statestore.StateStore, ctx context.Context, id, status string, deps ...string) {
	t.Helper()
	if err := store.CreateTask(ctx, statestore.Task{
		ID: id, ProjectID: "p1", Lane: "redesign", Tier: "T2", Status: status, ScenarioID: id, Deps: deps,
	}); err != nil {
		t.Fatalf("create task %s: %v", id, err)
	}
}

func leasePoll(t *testing.T, s *apiServer) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"host_id":"davinci","capabilities":["linux","web"]}`
	req := httptest.NewRequest(http.MethodPost, "/projects/p1/agent/lease", strings.NewReader(body))
	req.SetPathValue("id", "p1")
	rec := httptest.NewRecorder()
	s.handleAgentLease(rec, req)
	return rec
}

func statusOf(t *testing.T, store statestore.StateStore, ctx context.Context, id string) string {
	t.Helper()
	task, err := store.GetTask(ctx, id)
	if err != nil {
		t.Fatalf("get task %s: %v", id, err)
	}
	return task.Status
}

// The deadlock that put xirigo-vendor to sleep for 14 hours: every task stranded "running", the one
// remaining task blocked behind a stranded dependency, so PickReady had nothing to hand out and the
// project never recovered. A sweep that only ran after a successful pick would never have fired here
// — which is exactly why the sweep runs BEFORE the pick, on every poll.
func TestAgentLease_FreesPhantomsEvenWhenNothingIsPickable(t *testing.T) {
	s, store, ctx := phantomFixture(t)
	mustTask(t, store, ctx, "V-21", "running")            // stranded: no lease behind it
	mustTask(t, store, ctx, "V-39", "running")            // stranded
	mustTask(t, store, ctx, "V-40", "todo", "V-39")       // pickable only once V-39 lands

	rec := leasePoll(t, s)

	// The sweep runs BEFORE the pick, so recovery costs a single poll: the phantoms go back to
	// ready and one of them is handed straight back to the agent. The project is working again on
	// the very next request — no second round-trip, no operator, no restart.
	if rec.Code != http.StatusOK {
		t.Fatalf("lease: want 200 (a freed phantom is immediately pickable), got %d: %s", rec.Code, rec.Body.String())
	}
	if got := statusOf(t, store, ctx, "V-21"); got != "running" {
		t.Errorf("V-21: want running (freed, then leased in the same poll), got %q", got)
	}
	if got := statusOf(t, store, ctx, "V-39"); got != "ready" {
		t.Errorf("phantom V-39: want ready after the sweep, got %q", got)
	}
	// V-40 stays parked behind its dependency — the sweep frees phantoms, it does not invent work.
	if got := statusOf(t, store, ctx, "V-40"); got != "todo" {
		t.Errorf("V-40: want todo (dep V-39 has not landed), got %q", got)
	}
}

// The sweep must never abandon a task that is genuinely being worked: the leased task keeps its
// lease AND its running status. This is the test that makes the sweep safe to run on every poll.
func TestAgentLease_NeverRevertsTheLeasedTask(t *testing.T) {
	s, store, ctx := phantomFixture(t)
	mustTask(t, store, ctx, "A-1", "ready")

	// First poll leases A-1 and marks it running.
	if rec := leasePoll(t, s); rec.Code != http.StatusOK {
		t.Fatalf("first lease: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := statusOf(t, store, ctx, "A-1"); got != "running" {
		t.Fatalf("after lease: want A-1 running, got %q", got)
	}

	// A second poll arrives while A-1 is still being developed (the agent heartbeats, another
	// process polls, whatever). The sweep runs — and must leave the live task exactly as it is.
	leasePoll(t, s)

	if got := statusOf(t, store, ctx, "A-1"); got != "running" {
		t.Errorf("the LEASED task was reverted by the sweep: want running, got %q", got)
	}
	if l, err := store.GetLease(ctx, "p1"); err != nil || l.TaskID != "A-1" {
		t.Errorf("the lease on the live task was dropped: %+v (err=%v)", l, err)
	}
}

// A phantom coexisting with a LIVE lease on a different task is the case no API could reach:
// /retry rejects it (not blocked) and /agent/lease/release no-ops (not the owner, lease present).
// The sweep is the only thing that frees it — and it must do so without disturbing the live task.
func TestAgentLease_FreesPhantomWhileAnotherTaskHoldsTheLease(t *testing.T) {
	s, store, ctx := phantomFixture(t)
	mustTask(t, store, ctx, "A-1", "ready")
	mustTask(t, store, ctx, "A-22", "running") // phantom: stranded from an earlier run

	if rec := leasePoll(t, s); rec.Code != http.StatusOK {
		t.Fatalf("lease: want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if got := statusOf(t, store, ctx, "A-22"); got != "ready" {
		t.Errorf("phantom A-22: want ready, got %q", got)
	}
	if got := statusOf(t, store, ctx, "A-1"); got != "running" {
		t.Errorf("leased A-1: want running, got %q", got)
	}
}

// An approved task waiting for its merge is NOT running, and the merge path depends on that status
// surviving. Pin it: the sweep only ever touches "running".
func TestAgentLease_LeavesNonRunningStatusesAlone(t *testing.T) {
	s, store, ctx := phantomFixture(t)
	mustTask(t, store, ctx, "A-1", "ready")
	mustTask(t, store, ctx, "A-9", "awaiting-approval")
	mustTask(t, store, ctx, "A-8", "blocked")
	mustTask(t, store, ctx, "A-7", "done")

	leasePoll(t, s)

	for id, want := range map[string]string{"A-9": "awaiting-approval", "A-8": "blocked", "A-7": "done"} {
		if got := statusOf(t, store, ctx, id); got != want {
			t.Errorf("%s: the sweep changed a non-running task: want %q, got %q", id, want, got)
		}
	}
}
