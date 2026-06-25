package main

import (
	"context"
	"testing"

	"github.com/everva/conductor-platform/internal/intake"
	"github.com/everva/conductor-platform/internal/statestore"
)

// TestUniquifyScenarioIDs proves the director's approved scenario can no longer be
// silently dropped by intake: an ID that collides with existing project work is
// renamed so it lands on the board (the real "approved but nothing appeared" bug).
func TestUniquifyScenarioIDs(t *testing.T) {
	store := statestore.NewMemoryStore()
	ctx := context.Background()
	mustCreate(t, store.CreateProject(ctx, statestore.Project{
		ID: "optiway", Repo: "everva/optiway", BaseBranch: "develop", Readiness: "ready",
	}))
	// An old task already owns "A-1" (mirrors the stale test scenario in prod).
	mustCreate(t, store.CreateTask(ctx, statestore.Task{
		ID: "A-1", ProjectID: "optiway", Lane: "general", Tier: "T2", Status: "done",
	}))
	s := &apiServer{store: store, token: testToken, clock: fixedClock}

	// 1) A colliding ID is renamed; the derived pg://holdouts/<id> ref follows.
	out := s.uniquifyScenarioIDs(ctx, "optiway", []intake.Scenario{
		{ID: "A-1", Title: "remove ServiceCompany", Lane: "general", Tier: "T2",
			Acceptance: []string{"migration up/down green"}, HoldoutRef: "pg://holdouts/A-1"},
	})
	if out[0].ID != "A-1-2" {
		t.Fatalf("collision not renamed: got %q, want A-1-2", out[0].ID)
	}
	if out[0].HoldoutRef != "pg://holdouts/A-1-2" {
		t.Fatalf("derived holdout ref not synced: %q", out[0].HoldoutRef)
	}

	// 2) A fresh ID is untouched, and an explicit (non-derived) ref is preserved.
	out2 := s.uniquifyScenarioIDs(ctx, "optiway", []intake.Scenario{
		{ID: "FRESH-1", Title: "t", Lane: "web", Tier: "T2",
			Acceptance: []string{"x"}, HoldoutRef: "store://holdouts/FRESH-1/h.go"},
	})
	if out2[0].ID != "FRESH-1" || out2[0].HoldoutRef != "store://holdouts/FRESH-1/h.go" {
		t.Fatalf("non-colliding scenario mutated: %+v", out2[0])
	}

	// 3) Intra-batch duplicate IDs are disambiguated (the second is renamed).
	out3 := s.uniquifyScenarioIDs(ctx, "optiway", []intake.Scenario{
		{ID: "NEW-1", Title: "a", Lane: "web", Tier: "T2", Acceptance: []string{"x"}, HoldoutRef: "pg://holdouts/NEW-1"},
		{ID: "NEW-1", Title: "b", Lane: "web", Tier: "T2", Acceptance: []string{"x"}, HoldoutRef: "pg://holdouts/NEW-1"},
	})
	if out3[0].ID != "NEW-1" || out3[1].ID != "NEW-1-2" {
		t.Fatalf("intra-batch dedup: got %q,%q want NEW-1,NEW-1-2", out3[0].ID, out3[1].ID)
	}
}
