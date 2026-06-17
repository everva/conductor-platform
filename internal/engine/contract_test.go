package engine

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	"github.com/everva/conductor-platform/internal/statestore"
)

// TestVerdictJSONKeys asserts the frozen Verdict wire contract: marshalling a
// Verdict produces EXACTLY the keys the performer protocol (ADR-0014) and
// PHASE-1-PLAN §6 mandate — no more, no fewer. The hidden holdout (ADR-0018)
// pins this same set, so any drift here is a contract break.
func TestVerdictJSONKeys(t *testing.T) {
	v := Verdict{
		Result:    "pass",
		Branch:    "conductor/builder/PRE-0",
		CommitSHA: "deadbeef",
		Checks: []Check{
			{Name: "go build", Result: "pass", Evidence: "exit 0"},
		},
		Files:         []string{"internal/engine/engine.go"},
		BlockedReason: "",
		Summary:       "froze the contracts",
	}

	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal Verdict: %v", err)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal Verdict: %v", err)
	}

	want := []string{"result", "branch", "commit_sha", "checks", "files", "blocked_reason", "summary"}
	assertExactKeys(t, "Verdict", got, want)
}

// TestCheckJSONKeys pins the nested Check element shape {name, result, evidence}.
func TestCheckJSONKeys(t *testing.T) {
	b, err := json.Marshal(Check{Name: "go vet", Result: "pass", Evidence: "exit 0"})
	if err != nil {
		t.Fatalf("marshal Check: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal Check: %v", err)
	}
	assertExactKeys(t, "Check", got, []string{"name", "result", "evidence"})
}

// TestReviewResultJSONKeys pins the fresh-eyes review wire shape (ADR-0003).
func TestReviewResultJSONKeys(t *testing.T) {
	b, err := json.Marshal(ReviewResult{
		Result:   "changes-requested",
		Findings: []string{"facade binding missing"},
		Summary:  "needs work",
	})
	if err != nil {
		t.Fatalf("marshal ReviewResult: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal ReviewResult: %v", err)
	}
	assertExactKeys(t, "ReviewResult", got, []string{"result", "findings", "summary"})
}

// TestVerdictRoundTrip confirms a Verdict survives a marshal→unmarshal cycle
// without field loss.
func TestVerdictRoundTrip(t *testing.T) {
	want := Verdict{
		Result:        "fail",
		Branch:        "conductor/builder/PRE-0",
		CommitSHA:     "",
		Checks:        []Check{{Name: "go test", Result: "fail", Evidence: "TestX failed"}},
		Files:         []string{"a.go", "b.go"},
		BlockedReason: "",
		Summary:       "tests red",
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Verdict
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Result != want.Result || got.Branch != want.Branch || got.Summary != want.Summary {
		t.Fatalf("scalar mismatch: got %+v want %+v", got, want)
	}
	if len(got.Checks) != 1 || got.Checks[0].Name != "go test" || got.Checks[0].Result != "fail" {
		t.Fatalf("checks mismatch: got %+v", got.Checks)
	}
	if len(got.Files) != 2 {
		t.Fatalf("files mismatch: got %+v", got.Files)
	}
}

func assertExactKeys(t *testing.T, what string, got map[string]json.RawMessage, want []string) {
	t.Helper()
	gotKeys := make([]string, 0, len(got))
	for k := range got {
		gotKeys = append(gotKeys, k)
	}
	sort.Strings(gotKeys)
	sortedWant := append([]string(nil), want...)
	sort.Strings(sortedWant)
	if len(gotKeys) != len(sortedWant) {
		t.Fatalf("%s: key count = %d %v, want %d %v", what, len(gotKeys), gotKeys, len(sortedWant), sortedWant)
	}
	for i := range sortedWant {
		if gotKeys[i] != sortedWant[i] {
			t.Fatalf("%s: keys = %v, want %v", what, gotKeys, sortedWant)
		}
	}
}

// TestEngineAdapterIsImplementable is a compile-time guard: a no-op type can
// satisfy the frozen EngineAdapter signature set. It exercises no behaviour —
// implementations land in later tasks (ADR-0013 Dalga A).
func TestEngineAdapterIsImplementable(t *testing.T) {
	var _ EngineAdapter = stubAdapter{}
}

type stubAdapter struct{}

func (stubAdapter) Develop(context.Context, statestore.Task, Workspace) (Verdict, error) {
	return Verdict{}, nil
}
func (stubAdapter) Verify(context.Context, Verdict, Workspace) (ReviewResult, error) {
	return ReviewResult{}, nil
}
func (stubAdapter) Health(context.Context, Session) (HealthState, error) {
	return HealthState{}, nil
}
func (stubAdapter) Events(context.Context) (<-chan Event, error) { return nil, nil }
func (stubAdapter) Control(context.Context, Command) error       { return nil }
