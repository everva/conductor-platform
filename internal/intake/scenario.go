// Package intake turns the intake layer (ADR-0005, ADR-0012, ADR-0018) into a
// first-class, gate-testable part of the platform: it owns the RICH scenario
// schema, deterministic validation, a file-intake pipeline that persists
// scenarios + their tasks through the frozen statestore.StateStore, and the
// non-deterministic assisted-distillation step as an injectable Distiller seam
// with a deterministic parser.
//
// Division of responsibility vs the frozen contracts:
//
//   - statestore.Scenario (FROZEN) is the narrow projection persisted by the
//     registry/conductor. This package does NOT change it; it owns the rich form
//     (Scenario below — adds Title-free fields like the public-test reference,
//     the public-tests outline, and notes that have no home on the frozen type)
//     and projects down via ToStateScenario / ToStateTask, documenting the map.
//   - internal/engine (FROZEN) is mirrored, not reused: the Distiller borrows the
//     CommandEngine's "run a subprocess, parse structured output from prose"
//     pattern (ParseScenarios below is the deterministic analogue of
//     engine.ParseVerdict) but carries no engine type, so the engine signatures
//     stay frozen.
//
// The package is offline and deterministic except for the real-LLM Distiller
// path, which is excluded from the gate behind a build tag (see distiller_real.go).
package intake

import (
	"fmt"
	"strings"

	"github.com/everva/conductor-platform/internal/statestore"
)

// Scenario is the RICH intake-layer scenario (ADR-0012 schema + ADR-0018 holdout
// reference). It is the in-memory form a YAML scenario file or a Distiller run
// produces; it is validated here and then PROJECTED onto the frozen
// statestore.Scenario / statestore.Task for persistence.
//
// Fields beyond the frozen statestore.Scenario (PublicTestRef, PublicTestsOutline,
// Notes) are carried for transparency and operator review but have no slot on the
// frozen type; the projection drops them (see ToStateScenario). That is the
// documented narrowing — the frozen ledger stores only what the conductor needs to
// schedule and gate, while the repo's `.conductor/scenarios/` YAML keeps the full
// human-facing record (ADR-0012 §1).
type Scenario struct {
	// ID is the stable scenario identifier (e.g. "A-1"). Required.
	ID string `yaml:"id"`
	// Title is a human-readable summary. Required (the gate is empty without it).
	Title string `yaml:"title"`
	// Lane is the capability lane (ADR-0008) routing the task to a host. Required.
	Lane string `yaml:"lane"`
	// Tier is the risk tier (ADR-0003) gating merge mode. Required; one of T1..T4.
	Tier string `yaml:"tier"`
	// Deps lists scenario/task IDs that must land before this one is ready.
	Deps []string `yaml:"deps"`
	// Acceptance holds the human-authored acceptance criteria (ADR-0005 §1).
	// At least one is required — a scenario with no acceptance criterion leaves
	// the quality gate empty (ADR-0012 §Bağlam).
	Acceptance []string `yaml:"acceptance"`
	// HoldoutRef points at the repo-EXTERNAL hidden holdout (ADR-0018). It MUST
	// NOT be a repo-relative path: the holdout body never lives in the public
	// scenario. Required and must use an external locator scheme (see holdout.go).
	HoldoutRef string `yaml:"hidden_holdout_ref"`
	// PublicTestRef names the repo-internal public acceptance test file the
	// performer writes first (TDD, ADR-0012 karma görünürlük). Optional; carried
	// for transparency, not persisted on the frozen type.
	PublicTestRef string `yaml:"public_test_ref"`
	// PublicTestsOutline sketches the public tests. Optional; not persisted.
	PublicTestsOutline []string `yaml:"public_tests_outline"`
	// Notes is free-form operator context. Optional; not persisted.
	Notes string `yaml:"notes"`
}

// validTiers is the closed set of risk tiers (ADR-0003 T1..T4). Tier maps to the
// governance merge-mode policy, so it is a fixed vocabulary and an unknown tier is
// a hard validation error.
var validTiers = map[string]struct{}{
	"T1": {}, "T2": {}, "T3": {}, "T4": {},
}

// ToStateScenario projects the rich Scenario onto the FROZEN statestore.Scenario,
// binding it to projectID. The mapping is:
//
//	intake.Scenario.ID         -> statestore.Scenario.ID
//	(caller projectID)         -> statestore.Scenario.ProjectID
//	intake.Scenario.Title      -> statestore.Scenario.Title
//	intake.Scenario.Lane       -> statestore.Scenario.Lane
//	intake.Scenario.Tier       -> statestore.Scenario.Tier
//	intake.Scenario.Deps       -> statestore.Scenario.Deps
//	intake.Scenario.Acceptance -> statestore.Scenario.Acceptance
//	intake.Scenario.HoldoutRef -> statestore.Scenario.HoldoutRef (repo-external)
//
// The rich-only fields (PublicTestRef, PublicTestsOutline, Notes) have no slot on
// the frozen type and are intentionally dropped: they belong to the versioned
// `.conductor/scenarios/` YAML, not the scheduling ledger.
func (s Scenario) ToStateScenario(projectID string) statestore.Scenario {
	return statestore.Scenario{
		ID:         s.ID,
		ProjectID:  projectID,
		Title:      s.Title,
		Lane:       s.Lane,
		Tier:       s.Tier,
		Deps:       append([]string(nil), s.Deps...),
		Acceptance: append([]string(nil), s.Acceptance...),
		HoldoutRef: s.HoldoutRef,
	}
}

// ToStateTask projects the rich Scenario onto the FROZEN statestore.Task — the
// todo task created alongside the scenario. status is the initial lifecycle status
// the caller passes (registry.StatusTodo), kept as a parameter so this package
// carries no dependency on the registry's status vocabulary.
//
//	intake.Scenario.ID   -> statestore.Task.ID and statestore.Task.ScenarioID
//	(caller projectID)   -> statestore.Task.ProjectID
//	intake.Scenario.Lane -> statestore.Task.Lane
//	intake.Scenario.Tier -> statestore.Task.Tier
//	intake.Scenario.Deps -> statestore.Task.Deps
//	(caller status)      -> statestore.Task.Status
func (s Scenario) ToStateTask(projectID, status string) statestore.Task {
	return statestore.Task{
		ID:         s.ID,
		ProjectID:  projectID,
		Lane:       s.Lane,
		Tier:       s.Tier,
		Status:     status,
		Deps:       append([]string(nil), s.Deps...),
		ScenarioID: s.ID,
	}
}

// ValidationError aggregates every reason a scenario was rejected, so the operator
// sees ALL problems at once rather than fixing them one round-trip at a time. It is
// returned by Scenario.Validate when len(Reasons) > 0.
type ValidationError struct {
	// ScenarioID is the offending scenario's ID, or "" if even that was missing.
	ScenarioID string
	// Reasons is the non-empty list of human-readable rejection reasons.
	Reasons []string
}

// Error renders all rejection reasons in a single stable, comma-joined message.
func (e *ValidationError) Error() string {
	id := e.ScenarioID
	if id == "" {
		id = "<missing id>"
	}
	return fmt.Sprintf("scenario %q invalid: %s", id, strings.Join(e.Reasons, "; "))
}

// Validate checks a single scenario in isolation (no cross-scenario dep resolution
// — that is ValidateSet's job). It is deterministic and reports EVERY violation:
//
//   - missing id / title / lane,
//   - missing or unknown tier (not one of T1..T4),
//   - missing acceptance criteria (empty gate, ADR-0012),
//   - missing holdout reference, or a holdout reference that violates the
//     repo-external rule (ADR-0018 — the holdout body must not live in the repo).
//
// It returns nil when the scenario is well-formed, else a *ValidationError.
func (s Scenario) Validate() error {
	var reasons []string

	if strings.TrimSpace(s.ID) == "" {
		reasons = append(reasons, "missing id")
	}
	if strings.TrimSpace(s.Title) == "" {
		reasons = append(reasons, "missing title")
	}
	if strings.TrimSpace(s.Lane) == "" {
		reasons = append(reasons, "missing lane")
	}
	switch {
	case strings.TrimSpace(s.Tier) == "":
		reasons = append(reasons, "missing tier")
	default:
		if _, ok := validTiers[s.Tier]; !ok {
			reasons = append(reasons, fmt.Sprintf("unknown tier %q (want one of T1..T4)", s.Tier))
		}
	}
	if !hasNonBlank(s.Acceptance) {
		reasons = append(reasons, "missing acceptance criteria")
	}
	switch {
	case strings.TrimSpace(s.HoldoutRef) == "":
		reasons = append(reasons, "missing hidden_holdout_ref")
	default:
		if err := validateHoldoutRef(s.HoldoutRef); err != nil {
			reasons = append(reasons, err.Error())
		}
	}

	if len(reasons) == 0 {
		return nil
	}
	return &ValidationError{ScenarioID: s.ID, Reasons: reasons}
}

// ValidateSet validates a batch of scenarios together: each is checked in
// isolation via Validate, then cross-scenario rules run — duplicate IDs are
// rejected and every dep must resolve to an ID that is either already known (via
// the known set, e.g. tasks already in the store) OR present in THIS batch. A dep
// that resolves to nothing is "dangling" and rejected.
//
// known is the set of IDs that already exist outside this batch (typically the
// project's existing task IDs); pass nil for a standalone batch. The returned
// error, when non-nil, wraps the FIRST failing scenario's *ValidationError-style
// message but reports the specific cross-batch reason, so callers get a clear,
// deterministic rejection without a partial accept.
func ValidateSet(scenarios []Scenario, known map[string]struct{}) error {
	// Per-scenario shape first.
	for i := range scenarios {
		if err := scenarios[i].Validate(); err != nil {
			return err
		}
	}

	// Build the set of IDs visible to dep resolution: everything already known
	// plus every ID in this batch. Duplicate IDs within the batch are rejected.
	visible := make(map[string]struct{}, len(scenarios)+len(known))
	for id := range known {
		visible[id] = struct{}{}
	}
	seen := make(map[string]struct{}, len(scenarios))
	for _, s := range scenarios {
		if _, dup := seen[s.ID]; dup {
			return &ValidationError{ScenarioID: s.ID, Reasons: []string{"duplicate scenario id in batch"}}
		}
		seen[s.ID] = struct{}{}
		visible[s.ID] = struct{}{}
	}

	// Dangling-dep check against the visible universe. Self-deps are rejected.
	for _, s := range scenarios {
		for _, dep := range s.Deps {
			dep = strings.TrimSpace(dep)
			if dep == "" {
				continue
			}
			if dep == s.ID {
				return &ValidationError{ScenarioID: s.ID, Reasons: []string{"scenario depends on itself"}}
			}
			if _, ok := visible[dep]; !ok {
				return &ValidationError{
					ScenarioID: s.ID,
					Reasons:    []string{fmt.Sprintf("dangling dependency %q (not a known task or batch scenario)", dep)},
				}
			}
		}
	}
	return nil
}

// hasNonBlank reports whether ss contains at least one non-whitespace entry.
func hasNonBlank(ss []string) bool {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return true
		}
	}
	return false
}
