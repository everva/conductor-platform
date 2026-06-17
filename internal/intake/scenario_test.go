package intake

import (
	"errors"
	"strings"
	"testing"
)

// validScenario returns a fully well-formed scenario; tests mutate one field to
// exercise a single rejection reason at a time.
func validScenario() Scenario {
	return Scenario{
		ID:         "A-1",
		Title:      "do the thing",
		Lane:       "statestore",
		Tier:       "T1",
		Deps:       nil,
		Acceptance: []string{"it works"},
		HoldoutRef: "store://holdouts/A-1/holdout_test.go",
	}
}

func TestScenario_Validate_Table(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*Scenario)
		wantOK     bool
		wantReason string // substring expected in the error when wantOK is false.
	}{
		{name: "valid", mutate: func(*Scenario) {}, wantOK: true},
		{name: "valid_with_external_pg_ref", mutate: func(s *Scenario) {
			s.HoldoutRef = "pg://holdouts/42"
		}, wantOK: true},
		{name: "valid_with_private_ref", mutate: func(s *Scenario) {
			s.HoldoutRef = "private:holdouts#A-1"
		}, wantOK: true},

		{name: "missing_id", mutate: func(s *Scenario) { s.ID = "" }, wantReason: "missing id"},
		{name: "blank_id", mutate: func(s *Scenario) { s.ID = "   " }, wantReason: "missing id"},
		{name: "missing_title", mutate: func(s *Scenario) { s.Title = "" }, wantReason: "missing title"},
		{name: "missing_lane", mutate: func(s *Scenario) { s.Lane = "" }, wantReason: "missing lane"},
		{name: "missing_tier", mutate: func(s *Scenario) { s.Tier = "" }, wantReason: "missing tier"},
		{name: "unknown_tier", mutate: func(s *Scenario) { s.Tier = "T9" }, wantReason: "unknown tier"},
		{name: "lowercase_tier_rejected", mutate: func(s *Scenario) { s.Tier = "t1" }, wantReason: "unknown tier"},
		{name: "missing_acceptance", mutate: func(s *Scenario) { s.Acceptance = nil }, wantReason: "missing acceptance"},
		{name: "blank_only_acceptance", mutate: func(s *Scenario) { s.Acceptance = []string{"  "} }, wantReason: "missing acceptance"},
		{name: "missing_holdout", mutate: func(s *Scenario) { s.HoldoutRef = "" }, wantReason: "missing hidden_holdout_ref"},
		{name: "holdout_repo_relative_path", mutate: func(s *Scenario) {
			s.HoldoutRef = "internal/x/holdout_test.go"
		}, wantReason: "repo-external"},
		{name: "holdout_absolute_path", mutate: func(s *Scenario) {
			s.HoldoutRef = "/tmp/holdout_test.go"
		}, wantReason: "repo-external"},
		{name: "holdout_empty_scheme_body", mutate: func(s *Scenario) {
			s.HoldoutRef = "store://"
		}, wantReason: "no locator body"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validScenario()
			tt.mutate(&s)
			err := s.Validate()
			if tt.wantOK {
				if err != nil {
					t.Fatalf("expected valid, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected rejection containing %q, got nil", tt.wantReason)
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("expected *ValidationError, got %T: %v", err, err)
			}
			if !strings.Contains(err.Error(), tt.wantReason) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.wantReason)
			}
		})
	}
}

func TestScenario_Validate_AggregatesAllReasons(t *testing.T) {
	s := Scenario{} // everything missing.
	err := s.Validate()
	if err == nil {
		t.Fatal("expected error for empty scenario")
	}
	for _, want := range []string{"missing id", "missing title", "missing lane", "missing tier", "missing acceptance", "missing hidden_holdout_ref"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("aggregate error %q missing reason %q", err.Error(), want)
		}
	}
}

func TestValidateSet_DanglingDep(t *testing.T) {
	s := validScenario()
	s.Deps = []string{"NOPE"}
	err := ValidateSet([]Scenario{s}, nil)
	if err == nil || !strings.Contains(err.Error(), "dangling dependency") {
		t.Fatalf("expected dangling-dep rejection, got: %v", err)
	}
}

func TestValidateSet_IntraBatchDepResolves(t *testing.T) {
	a := validScenario()
	a.ID = "A-1"
	b := validScenario()
	b.ID = "A-2"
	b.Deps = []string{"A-1"} // resolves to a batch sibling.
	if err := ValidateSet([]Scenario{a, b}, nil); err != nil {
		t.Fatalf("intra-batch dep should resolve, got: %v", err)
	}
}

func TestValidateSet_KnownDepResolves(t *testing.T) {
	s := validScenario()
	s.Deps = []string{"PRE-0"}
	known := map[string]struct{}{"PRE-0": {}}
	if err := ValidateSet([]Scenario{s}, known); err != nil {
		t.Fatalf("known dep should resolve, got: %v", err)
	}
}

func TestValidateSet_DuplicateID(t *testing.T) {
	a := validScenario()
	b := validScenario() // same ID A-1.
	err := ValidateSet([]Scenario{a, b}, nil)
	if err == nil || !strings.Contains(err.Error(), "duplicate scenario id") {
		t.Fatalf("expected duplicate-id rejection, got: %v", err)
	}
}

func TestValidateSet_SelfDep(t *testing.T) {
	s := validScenario()
	s.Deps = []string{s.ID}
	err := ValidateSet([]Scenario{s}, nil)
	if err == nil || !strings.Contains(err.Error(), "depends on itself") {
		t.Fatalf("expected self-dep rejection, got: %v", err)
	}
}

func TestScenario_Projection_ToFrozenTypes(t *testing.T) {
	s := validScenario()
	s.Deps = []string{"PRE-0"}
	s.Notes = "dropped on projection"
	s.PublicTestRef = "internal/x_test.go"

	sc := s.ToStateScenario("proj")
	if sc.ID != "A-1" || sc.ProjectID != "proj" || sc.Lane != "statestore" || sc.Tier != "T1" {
		t.Fatalf("scenario projection wrong: %+v", sc)
	}
	if sc.HoldoutRef != s.HoldoutRef {
		t.Fatalf("holdout ref not projected: %q", sc.HoldoutRef)
	}
	if len(sc.Acceptance) != 1 || len(sc.Deps) != 1 || sc.Deps[0] != "PRE-0" {
		t.Fatalf("acceptance/deps not projected: %+v", sc)
	}

	task := s.ToStateTask("proj", StatusTodo)
	if task.ID != "A-1" || task.ScenarioID != "A-1" || task.ProjectID != "proj" || task.Status != StatusTodo {
		t.Fatalf("task projection wrong: %+v", task)
	}

	// Projection must produce independent slices (mutating the result must not
	// corrupt the source — the frozen MemoryStore stores copies but the projection
	// itself should not alias).
	sc.Deps[0] = "MUTATED"
	if s.Deps[0] != "PRE-0" {
		t.Fatalf("projection aliased the source deps slice")
	}
}
