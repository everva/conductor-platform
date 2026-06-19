package events

import (
	"context"
	"testing"
	"time"
)

// TestDiffSummary_Payload_RoundTrip proves the wire map a DiffSummary produces
// carries every field with the expected types (the shape a JSON round-trip
// through Postgres yields), so a consumer reading Event.Payload sees branch/base/
// patch/truncated plus a per-file list of maps. This is the DB-free contract test;
// the cross-process proof through real PG is below (gated on TEST_DATABASE_URL).
func TestDiffSummary_Payload_RoundTrip(t *testing.T) {
	s := DiffSummary{
		Branch: "conductor/T-1",
		Base:   "develop",
		Files: []DiffFile{
			{Path: "a.go", Status: "M", Additions: 3, Deletions: 1},
			{Path: "b.go", Status: "A", Additions: 10, Deletions: 0},
		},
		Patch:     "diff --git a/a.go b/a.go\n",
		Truncated: true,
	}
	p := s.Payload()
	if p["branch"] != "conductor/T-1" || p["base"] != "develop" {
		t.Fatalf("branch/base = %v/%v", p["branch"], p["base"])
	}
	if p["patch"] != s.Patch {
		t.Fatalf("patch = %v", p["patch"])
	}
	if p["truncated"] != true {
		t.Fatalf("truncated = %v, want true", p["truncated"])
	}
	files, ok := p["files"].([]any)
	if !ok {
		t.Fatalf("files not []any: %T", p["files"])
	}
	if len(files) != 2 {
		t.Fatalf("files len = %d, want 2", len(files))
	}
	first, ok := files[0].(map[string]any)
	if !ok {
		t.Fatalf("files[0] not map: %T", files[0])
	}
	if first["path"] != "a.go" || first["status"] != "M" || first["additions"] != 3 || first["deletions"] != 1 {
		t.Fatalf("files[0] = %+v", first)
	}
	// An empty file list must still be a non-nil empty slice (stable [] on the wire).
	empty := DiffSummary{Branch: "b", Base: "d"}.Payload()
	ef, ok := empty["files"].([]any)
	if !ok || ef == nil {
		t.Fatalf("empty files payload = %#v, want non-nil empty []any", empty["files"])
	}
	if len(ef) != 0 {
		t.Fatalf("empty files len = %d, want 0", len(ef))
	}
}

// TestPostgresKindDiffRoundTrip is the cross-process proof (ADR-0030, 4C-1): a
// KindDiff Event whose Payload is a DiffSummary.Payload() published through the
// REAL PostgresBus is delivered to a SEPARATE Subscribe() with the payload intact
// — branch/base/patch/truncated and the per-file list survive the PG JSON column
// + LISTEN/NOTIFY hop. It runs only when TEST_DATABASE_URL is set (skips
// otherwise, like the rest of this suite), so `go test ./...` stays green DB-free.
func TestPostgresKindDiffRoundTrip(t *testing.T) {
	bus := pgTestBus(t)
	ctx := context.Background()

	ch, cancel, err := bus.Subscribe(ctx, Filter{Project: "proj-diff"})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer cancel()

	// Let the LISTEN establish before publishing.
	time.Sleep(150 * time.Millisecond)

	summary := DiffSummary{
		Branch: "conductor/proj-diff/T-7",
		Base:   "develop",
		Files: []DiffFile{
			{Path: "src/x.go", Status: "M", Additions: 5, Deletions: 2},
			{Path: "src/y.go", Status: "A", Additions: 9, Deletions: 0},
		},
		Patch:     "diff --git a/src/x.go b/src/x.go\n@@ -1 +1 @@\n-old\n+new\n",
		Truncated: false,
	}
	want := Event{
		Project: "proj-diff",
		Task:    "T-7",
		Phase:   PhaseReview,
		Kind:    KindDiff,
		Payload: summary.Payload(),
	}
	if err := bus.Publish(ctx, want); err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case got, ok := <-ch:
		if !ok {
			t.Fatal("subscriber channel closed before delivery")
		}
		if got.Kind != KindDiff || got.Phase != PhaseReview {
			t.Fatalf("wrong kind/phase delivered: %+v", got)
		}
		if got.Project != "proj-diff" || got.Task != "T-7" {
			t.Fatalf("wrong envelope delivered: %+v", got)
		}
		// Scalar payload fields survive the JSON column + NOTIFY hop.
		if got.Payload["branch"] != summary.Branch || got.Payload["base"] != summary.Base {
			t.Fatalf("branch/base not delivered: %+v", got.Payload)
		}
		if got.Payload["patch"] != summary.Patch {
			t.Fatalf("patch not delivered: %+v", got.Payload["patch"])
		}
		if got.Payload["truncated"] != false {
			t.Fatalf("truncated not delivered: %+v", got.Payload["truncated"])
		}
		// The per-file list survives as a []any of maps; JSON numbers decode to
		// float64 out of the PG json column, so compare numerically.
		files, ok := got.Payload["files"].([]any)
		if !ok {
			t.Fatalf("files not a []any after PG round-trip: %T", got.Payload["files"])
		}
		if len(files) != 2 {
			t.Fatalf("files len = %d, want 2", len(files))
		}
		first, ok := files[0].(map[string]any)
		if !ok {
			t.Fatalf("files[0] not a map: %T", files[0])
		}
		if first["path"] != "src/x.go" || first["status"] != "M" {
			t.Fatalf("files[0] path/status = %+v, want src/x.go/M", first)
		}
		if !numEq(first["additions"], 5) || !numEq(first["deletions"], 2) {
			t.Fatalf("files[0] add/del = %v/%v, want 5/2", first["additions"], first["deletions"])
		}
		t.Logf("KindDiff round-tripped through PG: id=%s files=%d truncated=%v", got.ID, len(files), got.Payload["truncated"])
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for KindDiff LISTEN/NOTIFY delivery")
	}
}

// numEq compares a JSON-decoded numeric payload value (float64 out of PG's json
// column, or int when read straight from the in-memory map) against an expected
// int, so the round-trip assertion is robust to either decoding.
func numEq(got any, want int) bool {
	switch v := got.(type) {
	case float64:
		return v == float64(want)
	case int:
		return v == want
	case int64:
		return v == int64(want)
	default:
		return false
	}
}
