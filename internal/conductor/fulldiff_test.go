package conductor

import (
	"context"
	"errors"
	"testing"

	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/statestore"
)

// fakeFullDiffer is a fakeDiffer that ALSO implements FullDiffer (P2b, ADR-0041): the tick's
// emitDiff type-asserts it and persists the scripted FULL-context patch out-of-band via the
// store's TaskDiffStore seam. Embedding fakeDiffer reuses its scripted Diff.
type fakeFullDiffer struct {
	fakeDiffer
	fullPatch string
	truncated bool
	fullErr   error
	fullCalls int
}

func (f *fakeFullDiffer) FullPatch(_ context.Context, _ statestore.Project, _ engine.Workspace) (string, bool, error) {
	f.fullCalls++
	if f.fullErr != nil {
		return "", false, f.fullErr
	}
	return f.fullPatch, f.truncated, nil
}

// TestTick_PersistsFullDiff proves a green-gate tick whose Differ is ALSO a FullDiffer persists
// the FULL-context patch via TaskDiffStore (so the editor can fetch a full-file native diff), IN
// ADDITION to emitting the bounded KindDiff event, and still merges. The persisted range mirrors
// the verified branch vs the project base.
func TestTick_PersistsFullDiff(t *testing.T) {
	ctx := context.Background()
	em := &recordingEmitter{}
	differ := &fakeFullDiffer{
		fakeDiffer: fakeDiffer{summary: scriptedSummary()},
		fullPatch:  "FULL\nCONTEXT\nPATCH\n",
		truncated:  true,
	}
	cond, store, merge := condWithDiffer(t, em, differ, nil)

	res, err := cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if res.Outcome != OutcomeMerged {
		t.Fatalf("outcome = %q, want merged", res.Outcome)
	}
	if merge.calls != 1 {
		t.Fatalf("merge calls = %d, want 1 (full-diff persist must not block the merge)", merge.calls)
	}
	if differ.fullCalls != 1 {
		t.Fatalf("FullPatch calls = %d, want 1", differ.fullCalls)
	}
	got, err := store.GetTaskDiff(ctx, projectID, "T-1")
	if err != nil {
		t.Fatalf("GetTaskDiff: %v", err)
	}
	if got.Patch != "FULL\nCONTEXT\nPATCH\n" {
		t.Fatalf("persisted patch = %q, want the scripted full patch", got.Patch)
	}
	if !got.Truncated {
		t.Fatalf("persisted truncated = false, want true")
	}
	if got.Base != "develop" || got.Branch != "conductor/T-1" {
		t.Fatalf("persisted range = %s...%s, want develop...conductor/T-1", got.Base, got.Branch)
	}
}

// TestTick_NoFullDiffer_DoesNotPersist proves a Differ that is NOT a FullDiffer (bounded-only)
// persists nothing — the editor then falls back to the bounded KindDiff patch. The tick still
// emits the KindDiff event and merges (the existing behavior is unchanged).
func TestTick_NoFullDiffer_DoesNotPersist(t *testing.T) {
	ctx := context.Background()
	em := &recordingEmitter{}
	differ := &fakeDiffer{summary: scriptedSummary()} // Diff-only; NOT a FullDiffer.
	cond, store, _ := condWithDiffer(t, em, differ, nil)

	if _, err := cond.Tick(ctx, projectID); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if n := len(em.diffEvents()); n != 1 {
		t.Fatalf("want 1 KindDiff event emitted, got %d", n)
	}
	if _, err := store.GetTaskDiff(ctx, projectID, "T-1"); !errors.Is(err, statestore.ErrNotFound) {
		t.Fatalf("GetTaskDiff err = %v, want ErrNotFound (nothing persisted)", err)
	}
}

// TestTick_FullDiffError_SwallowedStillMerges proves a FullPatch error is observability-only:
// it is swallowed (nothing persisted) and the tick still merges exactly as without it.
func TestTick_FullDiffError_SwallowedStillMerges(t *testing.T) {
	ctx := context.Background()
	em := &recordingEmitter{}
	differ := &fakeFullDiffer{
		fakeDiffer: fakeDiffer{summary: scriptedSummary()},
		fullErr:    errors.New("git boom"),
	}
	cond, store, merge := condWithDiffer(t, em, differ, nil)

	res, err := cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if res.Outcome != OutcomeMerged || merge.calls != 1 {
		t.Fatalf("a full-diff error must not alter the tick: outcome=%q merges=%d", res.Outcome, merge.calls)
	}
	if _, err := store.GetTaskDiff(ctx, projectID, "T-1"); !errors.Is(err, statestore.ErrNotFound) {
		t.Fatalf("a failed full diff must persist nothing; got err=%v", err)
	}
}
