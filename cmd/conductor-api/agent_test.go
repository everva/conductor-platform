package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/everva/conductor-platform/internal/statestore"
)

// agentServer builds an apiServer over a fresh in-memory store with the fixed token +
// clock (no distiller needed for the agent-API).
func agentServer(t *testing.T) (*apiServer, statestore.StateStore) {
	t.Helper()
	store := statestore.NewMemoryStore()
	return &apiServer{store: store, token: testToken, clock: fixedClock}, store
}

// seedProject creates a ready project for agent tests.
func seedProject(t *testing.T, store statestore.StateStore, id string) {
	t.Helper()
	mustCreate(t, store.CreateProject(context.Background(), statestore.Project{
		ID: id, Repo: "owner/" + id, BaseBranch: "develop", Readiness: "ready",
	}))
}

// seedTodoTask creates a pickable (todo, no deps) task with the given requires.
func seedTodoTask(t *testing.T, store statestore.StateStore, proj, id string, requires []string) {
	t.Helper()
	mustCreate(t, store.CreateTask(context.Background(), statestore.Task{
		ID: id, ProjectID: proj, Lane: "backend", Tier: "T2", Status: "todo", Requires: requires,
	}))
}

func TestAgentLease_LeasesReadyTask(t *testing.T) {
	s, store := agentServer(t)
	ctx := context.Background()
	seedProject(t, store, "p")
	seedTodoTask(t, store, "p", "T-1", nil)

	rec := doBody(t, s, http.MethodPost, "/projects/p/agent/lease", bearer(),
		`{"host_id":"davinci","capabilities":["backend"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res agentLeaseResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if res.Task.ID != "T-1" || res.Lease.HostID != "davinci" || res.Lease.TaskID != "T-1" {
		t.Fatalf("unexpected lease response: %+v", res)
	}

	// The lease is actually held on the shared store (the gateway did the PG write).
	l, err := store.GetLease(ctx, "p")
	if err != nil || l.HostID != "davinci" || l.TaskID != "T-1" {
		t.Fatalf("lease not held by davinci: %+v err=%v", l, err)
	}
	// The host self-registered with its capabilities.
	h, err := store.GetHost(ctx, "davinci")
	if err != nil || len(h.Capabilities) != 1 || h.Capabilities[0] != "backend" {
		t.Fatalf("host not registered with caps: %+v err=%v", h, err)
	}
}

func TestAgentLease_NoWork204_StillRegisters(t *testing.T) {
	s, store := agentServer(t)
	seedProject(t, store, "p") // no tasks → nothing pickable

	rec := doBody(t, s, http.MethodPost, "/projects/p/agent/lease", bearer(),
		`{"host_id":"davinci","capabilities":["backend"]}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	// Even with no work, the host is registered (so its liveness is tracked).
	if _, err := store.GetHost(context.Background(), "davinci"); err != nil {
		t.Fatalf("host should register even on no-work: %v", err)
	}
}

func TestAgentLease_CapabilityRouting(t *testing.T) {
	s, store := agentServer(t)
	seedProject(t, store, "p")
	seedTodoTask(t, store, "p", "T-1", []string{"macos"}) // needs macos

	// A linux-only host cannot run it → no work (left for a capable host).
	rec := doBody(t, s, http.MethodPost, "/projects/p/agent/lease", bearer(),
		`{"host_id":"linbox","capabilities":["linux"]}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("linux host status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	// A macos host CAN run it → leased.
	rec = doBody(t, s, http.MethodPost, "/projects/p/agent/lease", bearer(),
		`{"host_id":"macbox","capabilities":["macos"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("macos host status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

func TestAgentLease_RepoBusy204(t *testing.T) {
	s, store := agentServer(t)
	seedProject(t, store, "p")
	seedTodoTask(t, store, "p", "T-1", nil)
	// Another host already holds the repo lease → repo-per-1 gate → nothing pickable.
	mustCreate(t, store.AcquireLease(context.Background(), statestore.Lease{
		ProjectID: "p", HostID: "other", TaskID: "T-9", AcquiredAt: fixedNow,
	}))

	rec := doBody(t, s, http.MethodPost, "/projects/p/agent/lease", bearer(),
		`{"host_id":"davinci","capabilities":["backend"]}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (repo busy); body=%s", rec.Code, rec.Body.String())
	}
}

func TestAgentLease_UnknownProject404(t *testing.T) {
	s, _ := agentServer(t)
	rec := doBody(t, s, http.MethodPost, "/projects/nope/agent/lease", bearer(),
		`{"host_id":"davinci"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestAgentLease_BadRequests(t *testing.T) {
	s, store := agentServer(t)
	seedProject(t, store, "p")

	if rec := doBody(t, s, http.MethodPost, "/projects/p/agent/lease", bearer(), `{"host_id":""}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty host_id status = %d, want 400", rec.Code)
	}
	if rec := doBody(t, s, http.MethodPost, "/projects/p/agent/lease", bearer(), `{not json`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json status = %d, want 400", rec.Code)
	}
	if rec := doBody(t, s, http.MethodPost, "/projects/p/agent/lease", "", `{"host_id":"davinci"}`); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-token status = %d, want 401", rec.Code)
	}
}

func TestAgentHeartbeat(t *testing.T) {
	s, store := agentServer(t)
	seedProject(t, store, "p")

	// Register the host via a lease call (no work → 204, but it registers).
	doBody(t, s, http.MethodPost, "/projects/p/agent/lease", bearer(), `{"host_id":"davinci","capabilities":["backend"]}`)

	if rec := doBody(t, s, http.MethodPost, "/agent/heartbeat", bearer(), `{"host_id":"davinci"}`); rec.Code != http.StatusOK {
		t.Fatalf("heartbeat status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// An unregistered host cannot heartbeat.
	if rec := doBody(t, s, http.MethodPost, "/agent/heartbeat", bearer(), `{"host_id":"ghost"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("ghost heartbeat status = %d, want 404", rec.Code)
	}
	if rec := doBody(t, s, http.MethodPost, "/agent/heartbeat", bearer(), `{"host_id":""}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty host_id status = %d, want 400", rec.Code)
	}
}

func TestAgentReleaseLease(t *testing.T) {
	s, store := agentServer(t)
	ctx := context.Background()
	seedProject(t, store, "p")
	seedTodoTask(t, store, "p", "T-1", nil)

	// Lease it.
	if rec := doBody(t, s, http.MethodPost, "/projects/p/agent/lease", bearer(), `{"host_id":"davinci","capabilities":["backend"]}`); rec.Code != http.StatusOK {
		t.Fatalf("lease status = %d, want 200", rec.Code)
	}

	// A non-owner release is an idempotent no-op: the lease stays.
	if rec := doBody(t, s, http.MethodPost, "/projects/p/agent/lease/release", bearer(), `{"host_id":"other","task_id":"T-1"}`); rec.Code != http.StatusOK {
		t.Fatalf("non-owner release status = %d, want 200", rec.Code)
	}
	if _, err := store.GetLease(ctx, "p"); err != nil {
		t.Fatalf("non-owner release must NOT drop the lease: %v", err)
	}

	// The owner release drops it.
	if rec := doBody(t, s, http.MethodPost, "/projects/p/agent/lease/release", bearer(), `{"host_id":"davinci","task_id":"T-1"}`); rec.Code != http.StatusOK {
		t.Fatalf("owner release status = %d, want 200", rec.Code)
	}
	if _, err := store.GetLease(ctx, "p"); err == nil {
		t.Fatalf("owner release must drop the lease")
	}
}
