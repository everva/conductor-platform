package statestore

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryEnhance_CreateClaimComplete(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

	// Create two pending jobs (j1 older than j2) for project p, and one for another project.
	if err := s.CreateEnhanceJob(ctx, EnhanceJob{ID: "j1", ProjectID: "p", RoughSpec: "a", CreatedAt: time.Unix(100, 0)}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateEnhanceJob(ctx, EnhanceJob{ID: "j2", ProjectID: "p", RoughSpec: "b", CreatedAt: time.Unix(200, 0)}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateEnhanceJob(ctx, EnhanceJob{ID: "j3", ProjectID: "other", RoughSpec: "c", CreatedAt: time.Unix(50, 0)}); err != nil {
		t.Fatal(err)
	}

	// Claim for p returns the OLDEST pending (j1), now running.
	got, ok, err := s.ClaimEnhanceJob(ctx, "p")
	if err != nil || !ok {
		t.Fatalf("claim p: ok=%v err=%v", ok, err)
	}
	if got.ID != "j1" || got.Status != EnhanceRunning {
		t.Fatalf("claim must return oldest pending j1 running, got %+v", got)
	}

	// Complete j1 with a result → done.
	if err := s.CompleteEnhanceJob(ctx, "j1", "detaylı spec", ""); err != nil {
		t.Fatal(err)
	}
	j1, err := s.GetEnhanceJob(ctx, "j1")
	if err != nil || j1.Status != EnhanceDone || j1.Result != "detaylı spec" {
		t.Fatalf("j1 must be done with result, got %+v err=%v", j1, err)
	}

	// Next claim for p returns j2 (j1 no longer pending).
	got, ok, _ = s.ClaimEnhanceJob(ctx, "p")
	if !ok || got.ID != "j2" {
		t.Fatalf("second claim must return j2, got ok=%v %+v", ok, got)
	}

	// No more pending for p.
	if _, ok, _ := s.ClaimEnhanceJob(ctx, "p"); ok {
		t.Fatalf("expected no more pending jobs for p")
	}

	// A failed completion records the error, no result.
	if err := s.CompleteEnhanceJob(ctx, "j2", "ignored", "boom"); err != nil {
		t.Fatal(err)
	}
	j2, _ := s.GetEnhanceJob(ctx, "j2")
	if j2.Status != EnhanceFailed || j2.Error != "boom" || j2.Result != "" {
		t.Fatalf("j2 must be failed with error and no result, got %+v", j2)
	}

	// Unknown id → ErrNotFound.
	if _, err := s.GetEnhanceJob(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown job must be ErrNotFound, got %v", err)
	}
}
