package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/everva/conductor-platform/internal/events"
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
	// M1: the leased task's STORED status is flipped to "running" so /tasks readers (the
	// editor's sessions tree) match the board's lease-derived "running" — no more mismatch.
	if res.Task.Status != "running" {
		t.Fatalf("lease response task status = %q, want running", res.Task.Status)
	}
	got, err := store.GetTask(ctx, "T-1")
	if err != nil || got.Status != "running" {
		t.Fatalf("stored task status = %q (err=%v), want running", got.Status, err)
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
	// A non-owner release must also NOT revert the running status — the owner is still
	// actively running the task (M1: only the owner's release reverts).
	if got, _ := store.GetTask(ctx, "T-1"); got.Status != "running" {
		t.Fatalf("non-owner release reverted status to %q, want running preserved", got.Status)
	}

	// The owner release drops it.
	if rec := doBody(t, s, http.MethodPost, "/projects/p/agent/lease/release", bearer(), `{"host_id":"davinci","task_id":"T-1"}`); rec.Code != http.StatusOK {
		t.Fatalf("owner release status = %d, want 200", rec.Code)
	}
	if _, err := store.GetLease(ctx, "p"); err == nil {
		t.Fatalf("owner release must drop the lease")
	}
	// M1: a release with no terminal verdict reverts the lease→running flip back to "ready"
	// so the abandoned task is re-runnable (not stranded as a hostless "running").
	if got, _ := store.GetTask(ctx, "T-1"); got.Status != "ready" {
		t.Fatalf("released running task status = %q, want ready (re-runnable)", got.Status)
	}
}

// TestAgentReleaseLease_DoesNotClobberTerminalStatus proves the M1 release-revert only
// touches a still-"running" task: a task the result path already moved to blocked (or
// awaiting-approval / done) keeps that status across the lease release.
func TestAgentReleaseLease_DoesNotClobberTerminalStatus(t *testing.T) {
	s, store := agentServer(t)
	ctx := context.Background()
	seedProject(t, store, "p")
	seedTodoTask(t, store, "p", "T-1", nil)

	// Lease (→ running), then the agent reports a failing gate → blocked.
	if rec := doBody(t, s, http.MethodPost, "/projects/p/agent/lease", bearer(), `{"host_id":"davinci","capabilities":["backend"]}`); rec.Code != http.StatusOK {
		t.Fatalf("lease status = %d, want 200", rec.Code)
	}
	if rec := doBody(t, s, http.MethodPost, "/projects/p/agent/tasks/T-1/result", bearer(), `{"result":"changes-requested","summary":"gate failed"}`); rec.Code != http.StatusOK {
		t.Fatalf("result status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got, _ := store.GetTask(ctx, "T-1"); got.Status != "blocked" {
		t.Fatalf("after blocked result status = %q, want blocked", got.Status)
	}

	// The owner release must NOT revert blocked → ready.
	if rec := doBody(t, s, http.MethodPost, "/projects/p/agent/lease/release", bearer(), `{"host_id":"davinci","task_id":"T-1"}`); rec.Code != http.StatusOK {
		t.Fatalf("owner release status = %d, want 200", rec.Code)
	}
	if got, _ := store.GetTask(ctx, "T-1"); got.Status != "blocked" {
		t.Fatalf("release clobbered terminal status to %q, want blocked preserved", got.Status)
	}
}

// seedRunningTask creates a task in the running state (post-lease) for result tests.
func seedRunningTask(t *testing.T, store statestore.StateStore, proj, id, tier string) {
	t.Helper()
	mustCreate(t, store.CreateTask(context.Background(), statestore.Task{
		ID: id, ProjectID: proj, Lane: "backend", Tier: tier, Status: "running",
	}))
}

// TestAgentResult_HeldForReview_FullFlow is the gateway-mediated held-for-review proof:
// pass → hold (record branch) → director /approve → agent /decision sees approved →
// agent /merged → done. No auto-merge of optiway's work ever happens at the gateway.
func TestAgentResult_HeldForReview_FullFlow(t *testing.T) {
	s, store := agentServer(t)
	ctx := context.Background()
	// Empty GovernancePolicy → fail-safe HELD (held-for-review, optiway's mode).
	mustCreate(t, store.CreateProject(ctx, statestore.Project{ID: "opt", Repo: "everva/optiway", BaseBranch: "conductor/optiway", Readiness: "ready"}))
	seedRunningTask(t, store, "opt", "T-1", "T2")

	rec := doBody(t, s, http.MethodPost, "/projects/opt/agent/tasks/T-1/result", bearer(),
		`{"result":"pass","branch":"conductor/opt/T-1","summary":"done","checks":[{"name":"go build","result":"pass","evidence":"exit 0"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("result status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var rr agentResultResponse
	json.Unmarshal(rec.Body.Bytes(), &rr) //nolint:errcheck
	if rr.Decision != "hold" {
		t.Fatalf("decision = %q, want hold", rr.Decision)
	}
	task, _ := store.GetTask(ctx, "T-1")
	if task.Status != "awaiting-approval" || task.Branch != "conductor/opt/T-1" || task.Approved {
		t.Fatalf("task not held with branch: %+v", task)
	}

	// Agent polls decision → pending.
	dec := do(t, s, http.MethodGet, "/projects/opt/agent/tasks/T-1/decision", bearer())
	if !decisionStateIs(t, dec, "pending") {
		t.Fatalf("decision before approve should be pending; body=%s", dec.Body.String())
	}

	// Director approves via the EXISTING control endpoint.
	if ap := doBody(t, s, http.MethodPost, "/projects/opt/approve", bearer(), `{"task_id":"T-1"}`); ap.Code != http.StatusOK {
		t.Fatalf("approve status = %d, want 200; body=%s", ap.Code, ap.Body.String())
	}
	dec2 := do(t, s, http.MethodGet, "/projects/opt/agent/tasks/T-1/decision", bearer())
	if !decisionStateIs(t, dec2, "approved") {
		t.Fatalf("decision after approve should be approved; body=%s", dec2.Body.String())
	}

	// Agent merged → task done, approval cleared.
	m := doBody(t, s, http.MethodPost, "/projects/opt/agent/tasks/T-1/merged", bearer(), `{"sha":"9f2c1ab"}`)
	if m.Code != http.StatusOK {
		t.Fatalf("merged status = %d, want 200; body=%s", m.Code, m.Body.String())
	}
	task, _ = store.GetTask(ctx, "T-1")
	if task.Status != "done" || task.Approved {
		t.Fatalf("task not done after merged: %+v", task)
	}
}

func decisionStateIs(t *testing.T, rec *httptest.ResponseRecorder, want string) bool {
	t.Helper()
	var dr agentDecisionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &dr); err != nil {
		t.Fatalf("decode decision: %v", err)
	}
	return dr.State == want
}

func TestAgentResult_AutoMerge(t *testing.T) {
	s, store := agentServer(t)
	mustCreate(t, store.CreateProject(context.Background(), statestore.Project{ID: "auto", Repo: "o/auto", BaseBranch: "main", Readiness: "ready", GovernancePolicy: "auto"}))
	seedRunningTask(t, store, "auto", "T-1", "T3") // even T3 auto-merges under "auto" policy

	rec := doBody(t, s, http.MethodPost, "/projects/auto/agent/tasks/T-1/result", bearer(),
		`{"result":"pass","branch":"b","summary":"ok"}`)
	var rr agentResultResponse
	json.Unmarshal(rec.Body.Bytes(), &rr) //nolint:errcheck
	if rec.Code != http.StatusOK || rr.Decision != "merge" {
		t.Fatalf("auto-merge: status=%d decision=%q, want 200/merge; body=%s", rec.Code, rr.Decision, rec.Body.String())
	}
}

func TestAgentResult_RiskLayered(t *testing.T) {
	s, store := agentServer(t)
	mustCreate(t, store.CreateProject(context.Background(), statestore.Project{ID: "rl", Repo: "o/rl", BaseBranch: "main", Readiness: "ready", GovernancePolicy: "risk-layered"}))
	seedRunningTask(t, store, "rl", "T-low", "T1")
	seedRunningTask(t, store, "rl", "T-high", "T4")

	low := doBody(t, s, http.MethodPost, "/projects/rl/agent/tasks/T-low/result", bearer(), `{"result":"pass","branch":"b"}`)
	var lr agentResultResponse
	json.Unmarshal(low.Body.Bytes(), &lr) //nolint:errcheck
	if lr.Decision != "merge" {
		t.Fatalf("T1 risk-layered decision = %q, want merge", lr.Decision)
	}
	high := doBody(t, s, http.MethodPost, "/projects/rl/agent/tasks/T-high/result", bearer(), `{"result":"pass","branch":"b"}`)
	var hr agentResultResponse
	json.Unmarshal(high.Body.Bytes(), &hr) //nolint:errcheck
	if hr.Decision != "hold" {
		t.Fatalf("T4 risk-layered decision = %q, want hold", hr.Decision)
	}
}

func TestAgentResult_Blocked(t *testing.T) {
	s, store := agentServer(t)
	mustCreate(t, store.CreateProject(context.Background(), statestore.Project{ID: "p", Repo: "o/p", BaseBranch: "main", Readiness: "ready"}))
	seedRunningTask(t, store, "p", "T-1", "T2")

	rec := doBody(t, s, http.MethodPost, "/projects/p/agent/tasks/T-1/result", bearer(), `{"result":"changes-requested","summary":"tests failed"}`)
	var rr agentResultResponse
	json.Unmarshal(rec.Body.Bytes(), &rr) //nolint:errcheck
	if rec.Code != http.StatusOK || rr.Decision != "blocked" {
		t.Fatalf("blocked: status=%d decision=%q; body=%s", rec.Code, rr.Decision, rec.Body.String())
	}
	task, _ := store.GetTask(context.Background(), "T-1")
	if task.Status != "blocked" {
		t.Fatalf("task status = %q, want blocked (never fake-green)", task.Status)
	}
	// The block reason is persisted on the task so the board explains WHY it stalled.
	if task.LastError != "tests failed" {
		t.Fatalf("task LastError = %q, want the report summary %q", task.LastError, "tests failed")
	}
	// …and surfaces through the director-facing /tasks DTO as `reason`.
	list := do(t, s, http.MethodGet, "/projects/p/tasks", bearer())
	if !strings.Contains(list.Body.String(), `"reason":"tests failed"`) {
		t.Fatalf("/tasks DTO missing reason; body=%s", list.Body.String())
	}
	// Retry clears the reason so the board shows the task retrying clean.
	if rec := do(t, s, http.MethodPost, "/projects/p/tasks/T-1/retry", bearer()); rec.Code != http.StatusOK {
		t.Fatalf("retry status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got, _ := store.GetTask(context.Background(), "T-1"); got.Status != "ready" || got.LastError != "" {
		t.Fatalf("after retry: status=%q lastError=%q; want ready + cleared", got.Status, got.LastError)
	}
}

// TestAgentLease_ClearsStaleLastError proves a fresh claim (lease → running) wipes a stale block
// reason from a prior attempt, so the board never shows a running task next to an old failure.
func TestAgentLease_ClearsStaleLastError(t *testing.T) {
	s, store := agentServer(t)
	ctx := context.Background()
	seedProject(t, store, "p")
	seedTodoTask(t, store, "p", "T-1", nil)
	tk, _ := store.GetTask(ctx, "T-1")
	tk.LastError = "prior block reason"
	if err := store.UpdateTask(ctx, tk); err != nil {
		t.Fatalf("stamp stale reason: %v", err)
	}
	if rec := doBody(t, s, http.MethodPost, "/projects/p/agent/lease", bearer(), `{"host_id":"davinci","capabilities":["backend"]}`); rec.Code != http.StatusOK {
		t.Fatalf("lease status=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got, _ := store.GetTask(ctx, "T-1"); got.Status != "running" || got.LastError != "" {
		t.Fatalf("after fresh claim: status=%q lastError=%q; want running + cleared", got.Status, got.LastError)
	}
}

func TestAgentDecision_Aborted(t *testing.T) {
	s, store := agentServer(t)
	ctx := context.Background()
	mustCreate(t, store.CreateProject(ctx, statestore.Project{ID: "p", Repo: "o/p", BaseBranch: "main", Readiness: "ready"}))
	mustCreate(t, store.CreateTask(ctx, statestore.Task{ID: "T-1", ProjectID: "p", Lane: "backend", Tier: "T2", Status: "running", AbortRequested: true}))

	dec := do(t, s, http.MethodGet, "/projects/p/agent/tasks/T-1/decision", bearer())
	var dr agentDecisionResponse
	json.Unmarshal(dec.Body.Bytes(), &dr) //nolint:errcheck
	if dr.State != "aborted" {
		t.Fatalf("decision state = %q, want aborted", dr.State)
	}
}

func TestAgentResult_BadRequests(t *testing.T) {
	s, store := agentServer(t)
	mustCreate(t, store.CreateProject(context.Background(), statestore.Project{ID: "p", Repo: "o/p", BaseBranch: "main", Readiness: "ready"}))
	seedRunningTask(t, store, "p", "T-1", "T2")

	if rec := doBody(t, s, http.MethodPost, "/projects/p/agent/tasks/T-1/result", bearer(), `{"result":"bogus"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("bogus result status = %d, want 400", rec.Code)
	}
	if rec := doBody(t, s, http.MethodPost, "/projects/p/agent/tasks/ghost/result", bearer(), `{"result":"pass","branch":"b"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown task status = %d, want 404", rec.Code)
	}
	// pass + held but no branch → 400.
	if rec := doBody(t, s, http.MethodPost, "/projects/p/agent/tasks/T-1/result", bearer(), `{"result":"pass"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("held without branch status = %d, want 400", rec.Code)
	}
}

func TestAgentReport(t *testing.T) {
	store := statestore.NewMemoryStore()
	bus := events.NewMemoryBus()
	s := &apiServer{store: store, bus: bus, token: testToken, clock: fixedClock}

	rec := doBody(t, s, http.MethodPost, "/projects/p/agent/tasks/T-1/report", bearer(),
		`{"phase":"develop","kind":"progress","payload":{"pct":70}}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("report status = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	// An invalid phase/kind is a 400, not a silently-dropped event.
	if bad := doBody(t, s, http.MethodPost, "/projects/p/agent/tasks/T-1/report", bearer(), `{"phase":"bogus","kind":"nope"}`); bad.Code != http.StatusBadRequest {
		t.Fatalf("invalid event status = %d, want 400", bad.Code)
	}
}
