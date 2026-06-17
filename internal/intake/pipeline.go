package intake

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/everva/conductor-platform/internal/statestore"
	"gopkg.in/yaml.v3"
)

// StatusTodo is the initial lifecycle status the intake pipeline stamps onto a
// newly created task. It mirrors registry.StatusTodo by value (the conductorctl
// intake path uses the same), declared here so the intake package carries no
// dependency on the registry's vocabulary while staying in lockstep with it.
const StatusTodo = "todo"

// LoadFile reads one or many YAML scenarios from path. It supports both a single
// scenario document and a multi-document YAML stream (--- separated), which is how
// a Distiller-produced batch or a hand-authored set is carried in one file. It
// performs NO validation — that is the pipeline's job after loading — but it does
// reject a YAML stream that contains no scenario documents.
func LoadFile(path string) ([]Scenario, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator-supplied scenario path.
	if err != nil {
		return nil, fmt.Errorf("intake: open scenario %q: %w", path, err)
	}
	scenarios, err := LoadYAML(data)
	if err != nil {
		return nil, fmt.Errorf("intake: %q: %w", path, err)
	}
	return scenarios, nil
}

// LoadYAML decodes one or many scenarios from a YAML byte stream. Each document in
// the stream is decoded as one Scenario. Empty documents (e.g. a trailing ---) are
// skipped. It returns an error if no scenario document is present.
func LoadYAML(data []byte) ([]Scenario, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var out []Scenario
	for {
		var s Scenario
		err := dec.Decode(&s)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode scenario yaml: %w", err)
		}
		// A document that decoded to a fully-zero Scenario is an empty/blank doc
		// (e.g. a stray separator); skip it rather than emitting a phantom.
		if isZeroScenario(s) {
			continue
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil, errors.New("no scenario documents found")
	}
	return out, nil
}

// IntakeResult reports what a pipeline run persisted, so callers (conductorctl,
// tests) can render or assert the outcome. Created lists the scenario IDs newly
// written this run; Skipped lists IDs that already existed and were left
// untouched (idempotent re-intake).
type IntakeResult struct {
	// Created holds the IDs of scenarios+tasks written this run, in input order.
	Created []string
	// Skipped holds the IDs that already existed and were not re-written.
	Skipped []string
}

// Intake is the file-intake pipeline: load → validate (shape + dangling deps
// against the project's existing tasks AND this batch) → persist each scenario and
// its todo task through the FROZEN StateStore. It is the single place the rich
// schema is projected onto the frozen types (ToStateScenario / ToStateTask).
//
// Idempotency (mirroring conductorctl/onboard): a scenario whose ID already exists
// in the store is SKIPPED, not re-written or duplicated, so re-running intake on
// the same file is a no-op for already-present scenarios. Validation runs over the
// WHOLE batch BEFORE any write, so a batch with one bad scenario writes nothing
// (no partial state).
//
// projectID must reference an existing project; the function verifies it up front.
func Intake(ctx context.Context, store statestore.StateStore, projectID string, scenarios []Scenario) (IntakeResult, error) {
	if projectID == "" {
		return IntakeResult{}, errors.New("intake: project is required")
	}
	if _, err := store.GetProject(ctx, projectID); err != nil {
		return IntakeResult{}, fmt.Errorf("intake: project %q: %w", projectID, err)
	}
	if len(scenarios) == 0 {
		return IntakeResult{}, errors.New("intake: no scenarios to ingest")
	}

	// The dep-resolution universe is the project's existing task IDs (deps may
	// reference tasks already landed) plus this batch (deps may be intra-batch).
	known, err := existingTaskIDs(ctx, store, projectID)
	if err != nil {
		return IntakeResult{}, err
	}
	if err := ValidateSet(scenarios, known); err != nil {
		return IntakeResult{}, fmt.Errorf("intake: %w", err)
	}

	// Persist. Already-present scenario IDs are skipped for idempotency; the rest
	// are written scenario-then-task so a created task always has its scenario.
	var res IntakeResult
	for _, s := range scenarios {
		exists, err := scenarioExists(ctx, store, s.ID)
		if err != nil {
			return IntakeResult{}, err
		}
		if exists {
			res.Skipped = append(res.Skipped, s.ID)
			continue
		}
		if err := store.CreateScenario(ctx, s.ToStateScenario(projectID)); err != nil {
			return IntakeResult{}, fmt.Errorf("intake: create scenario %q: %w", s.ID, err)
		}
		if err := store.CreateTask(ctx, s.ToStateTask(projectID, StatusTodo)); err != nil {
			return IntakeResult{}, fmt.Errorf("intake: create task %q: %w", s.ID, err)
		}
		res.Created = append(res.Created, s.ID)
	}
	return res, nil
}

// IntakeFile is the convenience entry point: LoadFile + Intake. It is what a CLI
// front-end (conductorctl) delegates to so the load/validate/persist logic lives
// in ONE place.
func IntakeFile(ctx context.Context, store statestore.StateStore, projectID, path string) (IntakeResult, error) {
	scenarios, err := LoadFile(path)
	if err != nil {
		return IntakeResult{}, err
	}
	return Intake(ctx, store, projectID, scenarios)
}

// existingTaskIDs returns the set of task IDs already registered for projectID,
// used as the "known" universe for dangling-dep resolution.
func existingTaskIDs(ctx context.Context, store statestore.StateStore, projectID string) (map[string]struct{}, error) {
	tasks, err := store.ListTasks(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("intake: list tasks: %w", err)
	}
	set := make(map[string]struct{}, len(tasks))
	for _, t := range tasks {
		set[t.ID] = struct{}{}
	}
	return set, nil
}

// scenarioExists reports whether a scenario with id is already persisted,
// translating the frozen ErrNotFound sentinel into a clean boolean.
func scenarioExists(ctx context.Context, store statestore.StateStore, id string) (bool, error) {
	_, err := store.GetScenario(ctx, id)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, statestore.ErrNotFound) {
		return false, nil
	}
	return false, fmt.Errorf("intake: check scenario %q: %w", id, err)
}

// isZeroScenario reports whether every field of s is empty — a document that
// decoded to nothing (a blank/separator-only YAML doc).
func isZeroScenario(s Scenario) bool {
	return s.ID == "" && s.Title == "" && s.Lane == "" && s.Tier == "" &&
		len(s.Deps) == 0 && len(s.Acceptance) == 0 && s.HoldoutRef == "" &&
		s.PublicTestRef == "" && len(s.PublicTestsOutline) == 0 && s.Notes == ""
}
