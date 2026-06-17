package intake

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/everva/conductor-platform/internal/statestore"
)

func newStoreWithProject(t *testing.T, projectID string) statestore.StateStore {
	t.Helper()
	store := statestore.NewMemoryStore()
	if err := store.CreateProject(context.Background(), statestore.Project{ID: projectID, Repo: "owner/" + projectID, BaseBranch: "develop"}); err != nil {
		t.Fatalf("create project: %v", err)
	}
	return store
}

func TestIntakeFile_PersistsScenariosAndTasks(t *testing.T) {
	ctx := context.Background()
	store := newStoreWithProject(t, "proj")

	res, err := IntakeFile(ctx, store, "proj", filepath.Join("testdata", "valid_batch.yaml"))
	if err != nil {
		t.Fatalf("intake: %v", err)
	}
	if len(res.Created) != 2 || res.Created[0] != "A-1" || res.Created[1] != "A-2" {
		t.Fatalf("unexpected created list: %+v", res.Created)
	}
	if len(res.Skipped) != 0 {
		t.Fatalf("expected nothing skipped on first run, got: %+v", res.Skipped)
	}

	// Both scenarios persisted, with the rich fields projected onto the frozen type.
	a1, err := store.GetScenario(ctx, "A-1")
	if err != nil {
		t.Fatalf("A-1 scenario not persisted: %v", err)
	}
	if a1.ProjectID != "proj" || a1.Lane != "statestore" || a1.Tier != "T1" {
		t.Fatalf("A-1 scenario projection wrong: %+v", a1)
	}
	if a1.HoldoutRef != "store://holdouts/A-1/memory_store_holdout_test.go" {
		t.Fatalf("A-1 holdout ref not persisted: %q", a1.HoldoutRef)
	}
	if len(a1.Acceptance) != 2 {
		t.Fatalf("A-1 acceptance not persisted: %+v", a1.Acceptance)
	}

	// Tasks persisted, with the intra-batch dep carried.
	a2task, err := store.GetTask(ctx, "A-2")
	if err != nil {
		t.Fatalf("A-2 task not persisted: %v", err)
	}
	if a2task.Status != StatusTodo || a2task.ScenarioID != "A-2" {
		t.Fatalf("A-2 task wrong: %+v", a2task)
	}
	if len(a2task.Deps) != 1 || a2task.Deps[0] != "A-1" {
		t.Fatalf("A-2 dep not carried: %+v", a2task.Deps)
	}
}

func TestIntakeFile_Idempotent(t *testing.T) {
	ctx := context.Background()
	store := newStoreWithProject(t, "proj")
	path := filepath.Join("testdata", "valid_batch.yaml")

	if _, err := IntakeFile(ctx, store, "proj", path); err != nil {
		t.Fatalf("first intake: %v", err)
	}
	// Re-intake the SAME file: nothing new created, both skipped, no duplicates.
	res, err := IntakeFile(ctx, store, "proj", path)
	if err != nil {
		t.Fatalf("second intake: %v", err)
	}
	if len(res.Created) != 0 {
		t.Fatalf("re-intake created duplicates: %+v", res.Created)
	}
	if len(res.Skipped) != 2 {
		t.Fatalf("re-intake should skip both, got skipped=%+v", res.Skipped)
	}

	tasks, err := store.ListTasks(ctx, "proj")
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("idempotent re-intake duplicated tasks: have %d", len(tasks))
	}
}

func TestIntakeFile_DanglingDep_NoWrite(t *testing.T) {
	ctx := context.Background()
	store := newStoreWithProject(t, "proj")

	_, err := IntakeFile(ctx, store, "proj", filepath.Join("testdata", "dangling_dep.yaml"))
	if err == nil {
		t.Fatal("expected dangling-dep rejection")
	}
	// No partial write.
	if _, gerr := store.GetScenario(ctx, "B-1"); gerr == nil {
		t.Fatal("scenario must not be written on dangling-dep refusal")
	}
	if _, gerr := store.GetTask(ctx, "B-1"); gerr == nil {
		t.Fatal("task must not be written on dangling-dep refusal")
	}
}

func TestIntake_DepOnExistingTask_Resolves(t *testing.T) {
	ctx := context.Background()
	store := newStoreWithProject(t, "proj")
	// Seed an existing task the new scenario depends on.
	if err := store.CreateTask(ctx, statestore.Task{ID: "PRE-0", ProjectID: "proj", Status: StatusTodo}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	s := validScenario()
	s.ID = "A-9"
	s.Deps = []string{"PRE-0"}
	res, err := Intake(ctx, store, "proj", []Scenario{s})
	if err != nil {
		t.Fatalf("intake with existing-task dep: %v", err)
	}
	if len(res.Created) != 1 || res.Created[0] != "A-9" {
		t.Fatalf("unexpected created: %+v", res.Created)
	}
}

func TestIntake_UnknownProject_Errors(t *testing.T) {
	ctx := context.Background()
	store := statestore.NewMemoryStore() // no project registered.
	_, err := Intake(ctx, store, "ghost", []Scenario{validScenario()})
	if err == nil {
		t.Fatal("expected error for unknown project")
	}
}

func TestLoadFile_MissingFile_Errors(t *testing.T) {
	if _, err := LoadFile(filepath.Join("testdata", "does-not-exist.yaml")); err == nil {
		t.Fatal("expected error for missing file")
	}
}
