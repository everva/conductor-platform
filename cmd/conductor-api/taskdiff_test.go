package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/everva/conductor-platform/internal/statestore"
)

// taskDiffServer builds an apiServer over a memory store seeded with one persisted full-context
// diff (P2b, ADR-0041), so the GET /projects/{id}/tasks/{task}/diff endpoint can be driven.
func taskDiffServer(t *testing.T) *apiServer {
	t.Helper()
	store := statestore.NewMemoryStore()
	if err := store.PutTaskDiff(context.Background(), statestore.TaskDiff{
		ProjectID: "proj-a",
		TaskID:    "T-1",
		Base:      "develop",
		Branch:    "conductor/T-1",
		Patch:     "diff --git a/x b/x\n@@ -1 +1 @@\n-a\n+b\n",
		Truncated: false,
	}); err != nil {
		t.Fatalf("seed task diff: %v", err)
	}
	return &apiServer{store: store, token: testToken, clock: fixedClock}
}

func TestProjectTaskDiff_HappyPath(t *testing.T) {
	s := taskDiffServer(t)
	rec := do(t, s, http.MethodGet, "/projects/proj-a/tasks/T-1/diff", bearer())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var got taskDiffDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ProjectID != "proj-a" || got.TaskID != "T-1" || got.Base != "develop" || got.Branch != "conductor/T-1" {
		t.Fatalf("diff DTO mapped wrong: %+v", got)
	}
	if !strings.Contains(got.Patch, "diff --git a/x b/x") {
		t.Fatalf("patch not returned: %q", got.Patch)
	}
	if got.Truncated {
		t.Fatalf("truncated = true, want false")
	}
}

func TestProjectTaskDiff_NotFound(t *testing.T) {
	s := taskDiffServer(t)
	rec := do(t, s, http.MethodGet, "/projects/proj-a/tasks/absent/diff", bearer())
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (editor falls back to the bounded patch); body=%s", rec.Code, rec.Body.String())
	}
}

func TestProjectTaskDiff_Unauthorized(t *testing.T) {
	s := taskDiffServer(t)
	rec := do(t, s, http.MethodGet, "/projects/proj-a/tasks/T-1/diff", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// A store with no TaskDiffStore persistence → 501 (graceful), not a panic.
func TestProjectTaskDiff_NotImplementedWhenStoreLacksPersistence(t *testing.T) {
	s := &apiServer{store: failingStore{}, token: testToken, clock: fixedClock}
	rec := do(t, s, http.MethodGet, "/projects/proj-a/tasks/T-1/diff", bearer())
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 (store has no TaskDiffStore); body=%s", rec.Code, rec.Body.String())
	}
}
