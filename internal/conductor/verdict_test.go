package conductor

import (
	"context"
	"testing"

	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/events"
	"github.com/everva/conductor-platform/internal/registry"
	"github.com/everva/conductor-platform/internal/statestore"
)

// decisionsWithChecks returns the KindDecision events carrying a per-gate `checks`
// payload — the Verifier verdict (B1, ADR-0033), distinct from the retry/approved
// KindDecisions which carry no checks.
func (r *recordingEmitter) decisionsWithChecks() []events.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []events.Event
	for _, e := range r.evts {
		if e.Kind == events.KindDecision {
			if _, ok := e.Payload["checks"]; ok {
				out = append(out, e)
			}
		}
	}
	return out
}

// verdictCond wires a conductor with a checks-bearing verifier + a recording emitter
// over the shared fakes, so the emitted Verifier verdict can be asserted.
func verdictCond(t *testing.T, em Emitter, result string, checks []engine.Check) *Conductor {
	t.Helper()
	ctx := context.Background()
	store := statestore.NewMemoryStore()
	if err := store.CreateProject(ctx, statestore.Project{ID: projectID, Repo: "owner/repo", BaseBranch: "develop"}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := store.CreateTask(ctx, statestore.Task{ID: "T-1", ProjectID: projectID, Lane: "x", Tier: "T2", Status: registry.StatusReady, Branch: "conductor/T-1"}); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	cond, err := New(Deps{
		Store:       store,
		Picker:      registry.NewRegistry(store),
		Provisioner: &fakeProvisioner{},
		Engine:      &fakeEngine{verdict: engine.Verdict{Result: "pass"}},
		Verifier:    &fakeVerifier{result: result, checks: checks},
		Merger:      &fakeMerger{sha: "abc123"},
		HostID:      "host-1",
		Emitter:     em,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return cond
}

// TestConductor_Tick_EmitsVerifierVerdict_OnPass proves a green tick emits exactly
// ONE Verifier-verdict KindDecision at PhaseReview carrying result=pass + the real
// per-gate checks (the platform's trust differentiator: WHY it passed, machine-proven).
func TestConductor_Tick_EmitsVerifierVerdict_OnPass(t *testing.T) {
	ctx := context.Background()
	em := &recordingEmitter{}
	checks := []engine.Check{
		{Name: "go build", Result: "pass", Evidence: "exit 0"},
		{Name: "go test", Result: "pass", Evidence: "exit 0"},
		{Name: "hidden holdout", Result: "pass", Evidence: "exit 0"},
	}
	cond := verdictCond(t, em, "pass", checks)

	res, err := cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if res.Outcome != OutcomeMerged {
		t.Fatalf("outcome = %q, want merged", res.Outcome)
	}

	verdicts := em.decisionsWithChecks()
	if len(verdicts) != 1 {
		t.Fatalf("verdict events = %d, want exactly 1; kinds %v", len(verdicts), em.kinds())
	}
	ev := verdicts[0]
	if ev.Phase != events.PhaseReview || ev.Task != "T-1" || ev.Project != projectID {
		t.Fatalf("verdict envelope wrong: %+v", ev)
	}
	if ev.Payload["result"] != "pass" {
		t.Fatalf("verdict result = %v, want pass", ev.Payload["result"])
	}
	got, ok := ev.Payload["checks"].([]map[string]any)
	if !ok {
		t.Fatalf("checks payload type = %T, want []map[string]any", ev.Payload["checks"])
	}
	if len(got) != 3 {
		t.Fatalf("verdict checks = %d, want 3", len(got))
	}
	if got[0]["name"] != "go build" || got[0]["result"] != "pass" || got[0]["evidence"] != "exit 0" {
		t.Fatalf("checks[0] = %+v, want go build/pass/exit 0", got[0])
	}
	if err := ev.Validate(); err != nil {
		t.Fatalf("verdict event invalid (would not survive the bus): %v", err)
	}
}

// TestConductor_Tick_EmitsVerifierVerdict_OnChangesRequested proves the NEGATIVE
// case (Rule#9 / the differentiator): when the gate fails, the verdict still emits
// with result=changes-requested AND the FAILING check + its evidence, so the editor
// can show WHY it was blocked — not a vague failure.
func TestConductor_Tick_EmitsVerifierVerdict_OnChangesRequested(t *testing.T) {
	ctx := context.Background()
	em := &recordingEmitter{}
	checks := []engine.Check{
		{Name: "go build", Result: "pass", Evidence: "exit 0"},
		{Name: "go test", Result: "fail", Evidence: "FAIL: TestThing expected 2 got 1"},
	}
	cond := verdictCond(t, em, "changes-requested", checks)

	if _, err := cond.Tick(ctx, projectID); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	verdicts := em.decisionsWithChecks()
	if len(verdicts) != 1 {
		t.Fatalf("verdict events = %d, want exactly 1; kinds %v", len(verdicts), em.kinds())
	}
	ev := verdicts[0]
	if ev.Payload["result"] != "changes-requested" {
		t.Fatalf("verdict result = %v, want changes-requested", ev.Payload["result"])
	}
	got, _ := ev.Payload["checks"].([]map[string]any)
	if len(got) != 2 || got[1]["result"] != "fail" {
		t.Fatalf("verdict checks = %+v, want the failing go test surfaced", got)
	}
	if got[1]["evidence"] != "FAIL: TestThing expected 2 got 1" {
		t.Fatalf("failing check evidence = %v, want the real failure line", got[1]["evidence"])
	}
}
