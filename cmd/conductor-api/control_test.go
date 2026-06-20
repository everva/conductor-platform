package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/everva/conductor-platform/internal/conductor"
	"github.com/everva/conductor-platform/internal/statestore"
)

// doBody issues a request with an optional body and auth header against routes().
func doBody(t *testing.T, s *apiServer, method, path, authHeader, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, req)
	return rec
}

// emptyServer builds an apiServer over a fresh in-memory store (no fixtures),
// with the fixed test token + clock.
func emptyServer() (*apiServer, statestore.StateStore) {
	store := statestore.NewMemoryStore()
	return &apiServer{store: store, token: testToken, clock: fixedClock}, store
}

// --- onboard ---

func TestOnboardCreateAndIdempotent(t *testing.T) {
	s, store := emptyServer()
	ctx := context.Background()

	// First onboard creates → 201, project JSON with derived id + default branch.
	rec := doBody(t, s, http.MethodPost, "/projects", bearer(), `{"repo":"owner/name"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var p projectDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.ID != "name" || p.Repo != "owner/name" || p.BaseBranch != "develop" {
		t.Fatalf("onboard mapped wrong: %+v", p)
	}

	// Second onboard of the SAME repo → 200, no duplicate.
	rec2 := doBody(t, s, http.MethodPost, "/projects", bearer(), `{"repo":"owner/name","base_branch":"main"}`)
	if rec2.Code != http.StatusOK {
		t.Fatalf("idempotent status = %d, want 200; body=%s", rec2.Code, rec2.Body.String())
	}
	var p2 projectDTO
	if err := json.Unmarshal(rec2.Body.Bytes(), &p2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Returned unchanged: original develop branch, NOT the new "main".
	if p2.BaseBranch != "develop" {
		t.Errorf("idempotent should return existing project unchanged, got base=%q", p2.BaseBranch)
	}

	projs, err := store.ListProjects(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(projs) != 1 {
		t.Fatalf("project count = %d, want 1 (no duplicate)", len(projs))
	}

	// GET /projects reflects it.
	g := do(t, s, http.MethodGet, "/projects", bearer())
	var got []projectDTO
	if err := json.Unmarshal(g.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if len(got) != 1 || got[0].ID != "name" {
		t.Fatalf("GET /projects = %+v, want single name", got)
	}
}

func TestOnboardEmptyRepo400(t *testing.T) {
	s, _ := emptyServer()
	for _, body := range []string{`{"repo":""}`, `{}`, ``} {
		rec := doBody(t, s, http.MethodPost, "/projects", bearer(), body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %q: status = %d, want 400; body=%s", body, rec.Code, rec.Body.String())
		}
	}
}

func TestOnboardInvalidJSON400(t *testing.T) {
	s, _ := emptyServer()
	rec := doBody(t, s, http.MethodPost, "/projects", bearer(), `{not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// TestOnboardInvalidRepo400 covers the F4 hardening: a repo that could be
// mis-parsed as a git CLI option (leading "-") or carries whitespace/control
// chars is rejected before it reaches the daemon's `git clone`.
func TestOnboardInvalidRepo400(t *testing.T) {
	s, store := emptyServer()
	for _, repo := range []string{"--upload-pack=evil", "-x", "owner/ x", "owner/x\nmalicious"} {
		bodyBytes, _ := json.Marshal(map[string]string{"repo": repo})
		rec := doBody(t, s, http.MethodPost, "/projects", bearer(), string(bodyBytes))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("repo %q: status = %d, want 400; body=%s", repo, rec.Code, rec.Body.String())
		}
	}
	// A legitimate local path / owner-name still works (not rejected).
	rec := doBody(t, s, http.MethodPost, "/projects", bearer(), `{"repo":"owner/ok"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("valid repo: status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	if _, err := store.GetProject(context.Background(), projectIDForRepoControl("owner/ok")); err != nil {
		t.Fatalf("valid repo not created: %v", err)
	}
}

// TestOnboardInvalidBaseBranch400 proves an EXPLICIT base_branch that could be
// mis-parsed as a git option (leading "-") or carries whitespace/control chars is
// rejected before it reaches the daemon's `git fetch`/`git worktree`. An empty
// base_branch is fine — it defaults.
func TestOnboardInvalidBaseBranch400(t *testing.T) {
	s, _ := emptyServer()
	for _, branch := range []string{"--upload-pack=evil", "-x", "de velop", "develop\nx"} {
		bodyBytes, _ := json.Marshal(map[string]string{"repo": "owner/ok", "base_branch": branch})
		rec := doBody(t, s, http.MethodPost, "/projects", bearer(), string(bodyBytes))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("base_branch %q: status = %d, want 400; body=%s", branch, rec.Code, rec.Body.String())
		}
	}
	// An empty base_branch defaults and is accepted.
	rec := doBody(t, s, http.MethodPost, "/projects", bearer(), `{"repo":"owner/defaults-ok"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("default base_branch: status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
}

// --- intake ---

const validIntakeYAML = `id: A-1
title: "First task"
lane: backend
tier: T1
deps: []
acceptance:
  - "does the thing"
hidden_holdout_ref: "store://holdouts/A-1/holdout_test.go"
---
id: A-2
title: "Second task depends on first"
lane: backend
tier: T2
deps: ["A-1"]
acceptance:
  - "does another thing"
hidden_holdout_ref: "store://holdouts/A-2/holdout_test.go"
`

func TestIntakeHappyPath(t *testing.T) {
	s, store := emptyServer()
	ctx := context.Background()
	mustCreate(t, store.CreateProject(ctx, statestore.Project{ID: "proj-x", Repo: "owner/x", BaseBranch: "develop"}))

	rec := doBody(t, s, http.MethodPost, "/projects/proj-x/intake", bearer(), validIntakeYAML)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res intakeResultDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res.Created) != 2 {
		t.Fatalf("created = %v, want 2", res.Created)
	}

	// GET /projects/{id}/tasks reflects the ingested tasks.
	g := do(t, s, http.MethodGet, "/projects/proj-x/tasks", bearer())
	var tasks []taskDTO
	if err := json.Unmarshal(g.Body.Bytes(), &tasks); err != nil {
		t.Fatalf("decode tasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("tasks = %d, want 2", len(tasks))
	}

	// Re-intake is idempotent: both skipped.
	rec2 := doBody(t, s, http.MethodPost, "/projects/proj-x/intake", bearer(), validIntakeYAML)
	if rec2.Code != http.StatusOK {
		t.Fatalf("re-intake status = %d, want 200", rec2.Code)
	}
	var res2 intakeResultDTO
	if err := json.Unmarshal(rec2.Body.Bytes(), &res2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res2.Skipped) != 2 || len(res2.Created) != 0 {
		t.Fatalf("re-intake = %+v, want 2 skipped 0 created", res2)
	}
}

func TestIntakeInvalidYAML400(t *testing.T) {
	s, store := emptyServer()
	mustCreate(t, store.CreateProject(context.Background(), statestore.Project{ID: "proj-x", Repo: "owner/x", BaseBranch: "develop"}))

	rec := doBody(t, s, http.MethodPost, "/projects/proj-x/intake", bearer(), "::: not valid yaml :::")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestIntakeEmptyBody400(t *testing.T) {
	s, store := emptyServer()
	mustCreate(t, store.CreateProject(context.Background(), statestore.Project{ID: "proj-x", Repo: "owner/x", BaseBranch: "develop"}))

	rec := doBody(t, s, http.MethodPost, "/projects/proj-x/intake", bearer(), "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestIntakeUnknownProject404(t *testing.T) {
	s, _ := emptyServer()
	rec := doBody(t, s, http.MethodPost, "/projects/nope/intake", bearer(), validIntakeYAML)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// --- pause / resume ---

func TestPauseResumeRoundTrip(t *testing.T) {
	s, store := emptyServer()
	ctx := context.Background()
	mustCreate(t, store.CreateProject(ctx, statestore.Project{ID: "proj-x", Repo: "owner/x", BaseBranch: "develop"}))

	rec := doBody(t, s, http.MethodPost, "/projects/proj-x/pause", bearer(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("pause status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	assertPaused(t, store, "proj-x", true)

	// Idempotent double-pause.
	rec = doBody(t, s, http.MethodPost, "/projects/proj-x/pause", bearer(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("double-pause status = %d, want 200", rec.Code)
	}
	assertPaused(t, store, "proj-x", true)

	rec = doBody(t, s, http.MethodPost, "/projects/proj-x/resume", bearer(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("resume status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	assertPaused(t, store, "proj-x", false)

	// Idempotent double-resume.
	rec = doBody(t, s, http.MethodPost, "/projects/proj-x/resume", bearer(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("double-resume status = %d, want 200", rec.Code)
	}
	assertPaused(t, store, "proj-x", false)
}

func TestPauseUnknownProject404(t *testing.T) {
	s, _ := emptyServer()
	rec := doBody(t, s, http.MethodPost, "/projects/nope/pause", bearer(), "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func assertPaused(t *testing.T, store statestore.StateStore, id string, want bool) {
	t.Helper()
	p, err := store.GetProject(context.Background(), id)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if p.Paused != want {
		t.Fatalf("Paused = %v, want %v", p.Paused, want)
	}
}

// --- abort ---

func TestAbortNothingRunning409(t *testing.T) {
	s, store := emptyServer()
	mustCreate(t, store.CreateProject(context.Background(), statestore.Project{ID: "proj-x", Repo: "owner/x", BaseBranch: "develop"}))

	rec := doBody(t, s, http.MethodPost, "/projects/proj-x/abort", bearer(), "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
}

func TestAbortRunningTask200(t *testing.T) {
	s, store := emptyServer()
	ctx := context.Background()
	mustCreate(t, store.CreateProject(ctx, statestore.Project{ID: "proj-x", Repo: "owner/x", BaseBranch: "develop"}))
	mustCreate(t, store.CreateTask(ctx, statestore.Task{ID: "T-1", ProjectID: "proj-x", Lane: "backend", Tier: "T1", Status: "running"}))
	mustCreate(t, store.AcquireLease(ctx, statestore.Lease{ProjectID: "proj-x", HostID: "host-1", TaskID: "T-1", AcquiredAt: fixedNow}))

	rec := doBody(t, s, http.MethodPost, "/projects/proj-x/abort", bearer(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["aborted_task"] != "T-1" {
		t.Fatalf("aborted_task = %v, want T-1", got["aborted_task"])
	}
	// Store reflects the durable abort signal.
	tk, err := store.GetTask(ctx, "T-1")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if !tk.AbortRequested {
		t.Fatalf("AbortRequested = false, want true")
	}
}

// --- approve ---

func TestApproveNoAwaiting409(t *testing.T) {
	s, store := emptyServer()
	mustCreate(t, store.CreateProject(context.Background(), statestore.Project{ID: "proj-x", Repo: "owner/x", BaseBranch: "develop"}))

	rec := doBody(t, s, http.MethodPost, "/projects/proj-x/approve", bearer(), "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
}

// TestApproveUnknownProject404 covers the F2 fix: approve on a project that does
// not exist must be 404 (not a misleading 409 "no task awaiting approval", which
// would imply the project exists). The auto-resolve path lists tasks, and an
// unknown project lists empty with no error, so an explicit existence check is
// required to distinguish unknown-project from no-held-task.
func TestApproveUnknownProject404(t *testing.T) {
	s, _ := emptyServer()
	rec := doBody(t, s, http.MethodPost, "/projects/does-not-exist/approve", bearer(), "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestApproveAutoResolve200(t *testing.T) {
	s, store := emptyServer()
	ctx := context.Background()
	mustCreate(t, store.CreateProject(ctx, statestore.Project{ID: "proj-x", Repo: "owner/x", BaseBranch: "develop"}))
	mustCreate(t, store.CreateTask(ctx, statestore.Task{
		ID: "T-9", ProjectID: "proj-x", Lane: "backend", Tier: "T3",
		Status: conductor.StatusAwaitingApproval, Branch: "task/T-9",
	}))

	rec := doBody(t, s, http.MethodPost, "/projects/proj-x/approve", bearer(), "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["approved_task"] != "T-9" {
		t.Fatalf("approved_task = %v, want T-9", got["approved_task"])
	}
	tk, err := store.GetTask(ctx, "T-9")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if !tk.Approved {
		t.Fatalf("Approved = false, want true")
	}
}

func TestApproveSpecificTaskID200(t *testing.T) {
	s, store := emptyServer()
	ctx := context.Background()
	mustCreate(t, store.CreateProject(ctx, statestore.Project{ID: "proj-x", Repo: "owner/x", BaseBranch: "develop"}))
	mustCreate(t, store.CreateTask(ctx, statestore.Task{ID: "T-1", ProjectID: "proj-x", Status: conductor.StatusAwaitingApproval}))
	mustCreate(t, store.CreateTask(ctx, statestore.Task{ID: "T-2", ProjectID: "proj-x", Status: conductor.StatusAwaitingApproval}))

	// Two held → auto-resolve would be ambiguous (409), but explicit task_id works.
	ambiguous := doBody(t, s, http.MethodPost, "/projects/proj-x/approve", bearer(), "")
	if ambiguous.Code != http.StatusConflict {
		t.Fatalf("ambiguous status = %d, want 409; body=%s", ambiguous.Code, ambiguous.Body.String())
	}

	rec := doBody(t, s, http.MethodPost, "/projects/proj-x/approve", bearer(), `{"task_id":"T-2"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	tk, err := store.GetTask(ctx, "T-2")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if !tk.Approved {
		t.Fatalf("T-2 Approved = false, want true")
	}
}

// --- auth: every POST without a token is 401 ---

func TestControlEndpointsRequireAuth(t *testing.T) {
	s, store := emptyServer()
	mustCreate(t, store.CreateProject(context.Background(), statestore.Project{ID: "proj-x", Repo: "owner/x", BaseBranch: "develop"}))

	cases := []struct {
		method, path, body string
	}{
		{http.MethodPost, "/projects", `{"repo":"o/r"}`},
		{http.MethodPost, "/projects/proj-x/intake", validIntakeYAML},
		{http.MethodPost, "/projects/proj-x/pause", ""},
		{http.MethodPost, "/projects/proj-x/resume", ""},
		{http.MethodPost, "/projects/proj-x/abort", ""},
		{http.MethodPost, "/projects/proj-x/approve", ""},
	}
	for _, c := range cases {
		// No token → 401.
		rec := doBody(t, s, c.method, c.path, "", c.body)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s no-token: status = %d, want 401", c.method, c.path, rec.Code)
		}
		// Wrong token → 401.
		rec = doBody(t, s, c.method, c.path, "Bearer wrong", c.body)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s wrong-token: status = %d, want 401", c.method, c.path, rec.Code)
		}
	}
}
