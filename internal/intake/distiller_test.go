package intake

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// validDistillerOutput wraps a valid scenario block in narration + fences, the way
// the real model is prompted to (prose around a fenced YAML answer).
const validDistillerOutput = `I analyzed the conversation. Here is the distilled scenario.

` + scenariosFenceStart + `
scenarios:
  - id: A-1
    title: "In-memory StateStore"
    lane: statestore
    tier: T1
    deps: []
    acceptance:
      - "MemoryStore implements the frozen interface."
    hidden_holdout_ref: "store://holdouts/A-1/holdout_test.go"
` + scenariosFenceEnd + `

Let me know if you want changes.`

func TestParseScenarios_ValidBlock(t *testing.T) {
	got, err := ParseScenarios([]byte(validDistillerOutput))
	if err != nil {
		t.Fatalf("parse valid: %v", err)
	}
	if len(got) != 1 || got[0].ID != "A-1" || got[0].Lane != "statestore" {
		t.Fatalf("unexpected scenarios: %+v", got)
	}
}

func TestParseScenarios_LastBlockWins(t *testing.T) {
	out := "draft:\n" + scenariosFenceStart + "\nscenarios:\n  - id: DRAFT\n" + scenariosFenceEnd +
		"\nfinal answer:\n" + validDistillerOutput
	got, err := ParseScenarios([]byte(out))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(got) != 1 || got[0].ID != "A-1" {
		t.Fatalf("last-block rule not applied: %+v", got)
	}
}

func TestParseScenarios_NoBlock_IsNoScenarios(t *testing.T) {
	_, err := ParseScenarios([]byte("all done, everything looks great, tests pass!"))
	if !errors.Is(err, ErrNoScenarios) {
		t.Fatalf("expected ErrNoScenarios, got: %v", err)
	}
}

func TestParseScenarios_EmptyList_IsNoScenarios(t *testing.T) {
	out := scenariosFenceStart + "\nscenarios: []\n" + scenariosFenceEnd
	_, err := ParseScenarios([]byte(out))
	if !errors.Is(err, ErrNoScenarios) {
		t.Fatalf("expected ErrNoScenarios for empty list, got: %v", err)
	}
}

func TestParseScenarios_BadYAML_IsMalformed(t *testing.T) {
	out := scenariosFenceStart + "\nscenarios: [this is : not : valid\n" + scenariosFenceEnd
	_, err := ParseScenarios([]byte(out))
	if !errors.Is(err, ErrMalformedScenarios) {
		t.Fatalf("expected ErrMalformedScenarios, got: %v", err)
	}
}

func TestParseScenarios_InvalidScenario_IsMalformed(t *testing.T) {
	// Well-formed YAML, but the scenario fails validation (repo-internal holdout).
	out := scenariosFenceStart + `
scenarios:
  - id: A-1
    title: "x"
    lane: statestore
    tier: T1
    acceptance: ["ok"]
    hidden_holdout_ref: "internal/x/holdout_test.go"
` + scenariosFenceEnd
	_, err := ParseScenarios([]byte(out))
	if !errors.Is(err, ErrMalformedScenarios) {
		t.Fatalf("expected ErrMalformedScenarios for invalid scenario, got: %v", err)
	}
	if !strings.Contains(err.Error(), "repo-external") {
		t.Fatalf("error should surface the validation reason: %v", err)
	}
}

// stubRunner returns crafted stdout (and an optional error) without any real LLM,
// keeping the distiller test fully offline.
func stubRunner(stdout string, err error) distillRunner {
	return func(_ context.Context, _ string) ([]byte, error) {
		return []byte(stdout), err
	}
}

func TestCommandDistiller_Distill_Valid(t *testing.T) {
	d := NewCommandDistillerWithRunner(stubRunner(validDistillerOutput, nil))
	got, err := d.Distill(context.Background(), "we need an in-memory state store")
	if err != nil {
		t.Fatalf("distill: %v", err)
	}
	if len(got) != 1 || got[0].ID != "A-1" {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestCommandDistiller_Distill_MalformedOutput_NoScenarios(t *testing.T) {
	d := NewCommandDistillerWithRunner(stubRunner("here is my analysis, no block", nil))
	got, err := d.Distill(context.Background(), "conversation")
	if err == nil {
		t.Fatal("expected error on malformed output")
	}
	if !errors.Is(err, ErrNoScenarios) {
		t.Fatalf("expected ErrNoScenarios, got: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("must not fabricate scenarios, got: %+v", got)
	}
}

func TestCommandDistiller_Distill_RunErrorEmptyOutput_NoScenarios(t *testing.T) {
	d := NewCommandDistillerWithRunner(stubRunner("", errors.New("boom")))
	_, err := d.Distill(context.Background(), "conversation")
	if !errors.Is(err, ErrNoScenarios) {
		t.Fatalf("expected ErrNoScenarios on run error + empty output, got: %v", err)
	}
}

func TestCommandDistiller_Distill_RunErrorWithOutput_StillParses(t *testing.T) {
	// Non-zero exit but a valid block on stdout: still parsed (engine leniency).
	d := NewCommandDistillerWithRunner(stubRunner(validDistillerOutput, errors.New("exit 1")))
	got, err := d.Distill(context.Background(), "conversation")
	if err != nil {
		t.Fatalf("distill with usable output despite run error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 scenario, got: %+v", got)
	}
}

func TestCommandDistiller_EmptyConversation_Rejected(t *testing.T) {
	d := NewCommandDistillerWithRunner(stubRunner(validDistillerOutput, nil))
	if _, err := d.Distill(context.Background(), "   "); err == nil {
		t.Fatal("expected error for empty conversation")
	}
}

func TestCommandDistiller_NilRunner_Rejected(t *testing.T) {
	d := NewCommandDistillerWithRunner(nil)
	if _, err := d.Distill(context.Background(), "x"); err == nil {
		t.Fatal("expected error for nil runner")
	}
}
