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

func TestIntake_CrossProjectDep_Resolves(t *testing.T) {
	ctx := context.Background()
	// Frontend project + a SEPARATE backend project holding the dep task.
	store := newStoreWithProject(t, "proj-fe")
	if err := store.CreateProject(ctx, statestore.Project{ID: "proj-be", Repo: "owner/proj-be", BaseBranch: "develop"}); err != nil {
		t.Fatalf("seed backend project: %v", err)
	}
	if err := store.CreateTask(ctx, statestore.Task{ID: "B-endpoint-1", ProjectID: "proj-be", Status: StatusTodo}); err != nil {
		t.Fatalf("seed backend task: %v", err)
	}

	// A frontend F-wire scenario depending on the backend task (cross-project).
	s := validScenario()
	s.ID = "A-9-fwire"
	s.Deps = []string{"B-endpoint-1"}
	res, err := Intake(ctx, store, "proj-fe", []Scenario{s})
	if err != nil {
		t.Fatalf("cross-project dep must be admitted (backend task exists globally): %v", err)
	}
	if len(res.Created) != 1 || res.Created[0] != "A-9-fwire" {
		t.Fatalf("unexpected created: %+v", res.Created)
	}
	got, err := store.GetTask(ctx, "A-9-fwire")
	if err != nil {
		t.Fatalf("get created task: %v", err)
	}
	if len(got.Deps) != 1 || got.Deps[0] != "B-endpoint-1" {
		t.Fatalf("cross-project dep not persisted: %+v", got.Deps)
	}

	// A dep that exists in NO project is still a dangling typo → rejected, no write.
	bad := validScenario()
	bad.ID = "A-10-bad"
	bad.Deps = []string{"B-does-not-exist"}
	if _, err := Intake(ctx, store, "proj-fe", []Scenario{bad}); err == nil {
		t.Fatal("a globally-nonexistent dep must still be rejected as dangling")
	}
	if _, gerr := store.GetTask(ctx, "A-10-bad"); gerr == nil {
		t.Fatal("task must not be written on dangling-dep refusal")
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
