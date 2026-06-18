package statestore

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// storeFactory builds a fresh, empty StateStore for one subtest and returns a
// cleanup func. It lets the conformance suite run the identical assertions
// against every implementation (MemoryStore, PostgresStore) so each is proven to
// satisfy the frozen contract identically.
type storeFactory func(t *testing.T) (StateStore, func())

// TestConformance_Memory runs the shared suite against the in-memory store. It
// always runs (no external dependency).
func TestConformance_Memory(t *testing.T) {
	runConformanceSuite(t, func(t *testing.T) (StateStore, func()) {
		return NewMemoryStore(), func() {}
	})
}

// runConformanceSuite executes every conformance case against the store built by
// newStore. Each case gets its own fresh store via the factory.
func runConformanceSuite(t *testing.T, newStore storeFactory) {
	t.Helper()
	cases := []struct {
		name string
		fn   func(t *testing.T, s StateStore)
	}{
		{"ProjectCRUD", confProjectCRUD},
		{"UpdateProjectRoundTrip", confUpdateProject},
		{"UpdateMissingProjectFails", confUpdateMissingProject},
		{"TaskCRUD", confTaskCRUD},
		{"ScenarioCRUD", confScenarioCRUD},
		{"GetMissingReturnsErrNotFound", confGetMissing},
		{"DuplicateCreateFails", confDuplicateCreate},
		{"UpdateMissingTaskFails", confUpdateMissingTask},
		{"ListTasksScopedByProject", confListTasksScoped},
		{"ListOrderingStable", confListOrdering},
		{"SliceFieldsRoundTrip", confSliceRoundTrip},
		{"LeaseLifecycle", confLeaseLifecycle},
		{"AcquireLeaseSecondFails", confAcquireSecondFails},
		{"ReleaseLeaseIdempotent", confReleaseIdempotent},
		{"AcquireLeaseConcurrentOneWinner", confAcquireConcurrent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, cleanup := newStore(t)
			defer cleanup()
			tc.fn(t, s)
		})
	}
}

func confTask(id, projectID, status string) Task {
	return Task{
		ID:        id,
		ProjectID: projectID,
		Lane:      "statestore",
		Tier:      "T1",
		Status:    status,
		Requires:  []string{"go"},
		Deps:      []string{"PRE-0"},
		Branch:    "conductor/builder/" + id,
	}
}

func confProjectCRUD(t *testing.T, s StateStore) {
	ctx := context.Background()
	p := Project{
		ID: "p1", Repo: "owner/name", BaseBranch: "develop", HostID: "h1",
		Readiness: "ready", RecipePointer: ".conductor/recipe.yaml", GovernancePolicy: "tier-default",
	}
	if err := s.CreateProject(ctx, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	got, err := s.GetProject(ctx, "p1")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got != p {
		t.Fatalf("GetProject = %+v, want %+v", got, p)
	}
	list, err := s.ListProjects(ctx)
	if err != nil || len(list) != 1 || list[0].ID != "p1" {
		t.Fatalf("ListProjects = %+v, err %v", list, err)
	}

	// Empty id must be rejected.
	if err := s.CreateProject(ctx, Project{}); err == nil {
		t.Fatal("CreateProject empty id: want error, got nil")
	}
}

// confUpdateProject proves the ADR-0021 additive UpdateProject round-trips a
// full-row mutation through both stores, including the first-class pause field,
// and defaults to not-paused on CreateProject.
func confUpdateProject(t *testing.T, s StateStore) {
	ctx := context.Background()
	p := Project{
		ID: "p1", Repo: "owner/name", BaseBranch: "develop", HostID: "h1",
		Readiness: "ready", RecipePointer: ".conductor/recipe.yaml", GovernancePolicy: "tier-default",
	}
	if err := s.CreateProject(ctx, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	// CreateProject defaults to running (not paused).
	if got, _ := s.GetProject(ctx, "p1"); got.Paused {
		t.Fatalf("new project must default to not-paused, got %+v", got)
	}

	// Mutate every column, including the pause field, and persist it.
	p.Repo = "owner/renamed"
	p.HostID = "h2"
	p.Readiness = "degraded"
	p.GovernancePolicy = "tier-strict"
	p.Paused = true
	if err := s.UpdateProject(ctx, p); err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}
	got, err := s.GetProject(ctx, "p1")
	if err != nil {
		t.Fatalf("GetProject after update: %v", err)
	}
	if got != p {
		t.Fatalf("UpdateProject round-trip = %+v, want %+v", got, p)
	}
	if !got.Paused {
		t.Fatalf("pause field did not persist: %+v", got)
	}

	// Resume (clear pause) persists too.
	p.Paused = false
	if err := s.UpdateProject(ctx, p); err != nil {
		t.Fatalf("UpdateProject resume: %v", err)
	}
	if got, _ := s.GetProject(ctx, "p1"); got.Paused {
		t.Fatalf("resume did not persist: %+v", got)
	}
}

// confUpdateMissingProject proves UpdateProject on an unknown id is ErrNotFound in
// both stores (no silent insert).
func confUpdateMissingProject(t *testing.T, s StateStore) {
	ctx := context.Background()
	if err := s.UpdateProject(ctx, Project{ID: "ghost", Repo: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateProject missing: err = %v, want ErrNotFound", err)
	}
}

func confTaskCRUD(t *testing.T, s StateStore) {
	ctx := context.Background()
	if err := s.CreateTask(ctx, confTask("A-1", "p1", "ready")); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	got, err := s.GetTask(ctx, "A-1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != "ready" {
		t.Fatalf("status = %q, want ready", got.Status)
	}
	// A freshly created task defaults to not-aborting (F-2 additive field).
	if got.AbortRequested {
		t.Fatalf("fresh task must default AbortRequested=false, got %+v", got)
	}
	got.Status = "done"
	got.RetryCount = 2
	got.AbortRequested = true // F-2: round-trip the additive abort signal.
	if err := s.UpdateTask(ctx, got); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	reread, err := s.GetTask(ctx, "A-1")
	if err != nil {
		t.Fatalf("GetTask after update: %v", err)
	}
	if reread.Status != "done" || reread.RetryCount != 2 || !reread.AbortRequested {
		t.Fatalf("after update = %+v, want status=done retry=2 abort=true", reread)
	}
	list, err := s.ListTasks(ctx, "p1")
	if err != nil || len(list) != 1 || list[0].Status != "done" {
		t.Fatalf("ListTasks = %+v, err %v", list, err)
	}
}

func confScenarioCRUD(t *testing.T, s StateStore) {
	ctx := context.Background()
	sc := Scenario{
		ID: "S-1", ProjectID: "p1", Title: "title", Lane: "statestore", Tier: "T1",
		Deps: []string{"PRE-0"}, Acceptance: []string{"crit-a", "crit-b"}, HoldoutRef: "holdout://x",
	}
	if err := s.CreateScenario(ctx, sc); err != nil {
		t.Fatalf("CreateScenario: %v", err)
	}
	got, err := s.GetScenario(ctx, "S-1")
	if err != nil {
		t.Fatalf("GetScenario: %v", err)
	}
	if got.Title != "title" || len(got.Acceptance) != 2 || got.Acceptance[1] != "crit-b" || got.HoldoutRef != "holdout://x" {
		t.Fatalf("GetScenario = %+v", got)
	}
	list, err := s.ListScenarios(ctx, "p1")
	if err != nil || len(list) != 1 || list[0].ID != "S-1" {
		t.Fatalf("ListScenarios = %+v, err %v", list, err)
	}
}

func confGetMissing(t *testing.T, s StateStore) {
	ctx := context.Background()
	if _, err := s.GetProject(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetProject missing: err = %v, want ErrNotFound", err)
	}
	if _, err := s.GetTask(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetTask missing: err = %v, want ErrNotFound", err)
	}
	if _, err := s.GetLease(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetLease missing: err = %v, want ErrNotFound", err)
	}
	if _, err := s.GetScenario(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetScenario missing: err = %v, want ErrNotFound", err)
	}
}

func confDuplicateCreate(t *testing.T, s StateStore) {
	ctx := context.Background()
	if err := s.CreateProject(ctx, Project{ID: "p1", Repo: "a"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if err := s.CreateProject(ctx, Project{ID: "p1", Repo: "b"}); err == nil {
		t.Fatal("duplicate CreateProject: want error, got nil")
	}
	// Original untouched.
	if got, _ := s.GetProject(ctx, "p1"); got.Repo != "a" {
		t.Fatalf("duplicate create overwrote project: %+v", got)
	}

	if err := s.CreateTask(ctx, confTask("A-1", "p1", "ready")); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if err := s.CreateTask(ctx, confTask("A-1", "p1", "running")); err == nil {
		t.Fatal("duplicate CreateTask: want error, got nil")
	}
	if got, _ := s.GetTask(ctx, "A-1"); got.Status != "ready" {
		t.Fatalf("duplicate create overwrote task: %+v", got)
	}

	if err := s.CreateScenario(ctx, Scenario{ID: "S-1", ProjectID: "p1"}); err != nil {
		t.Fatalf("CreateScenario: %v", err)
	}
	if err := s.CreateScenario(ctx, Scenario{ID: "S-1", ProjectID: "p1"}); err == nil {
		t.Fatal("duplicate CreateScenario: want error, got nil")
	}
}

func confUpdateMissingTask(t *testing.T, s StateStore) {
	ctx := context.Background()
	if err := s.UpdateTask(ctx, confTask("ghost", "p1", "ready")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateTask missing: err = %v, want ErrNotFound", err)
	}
}

func confListTasksScoped(t *testing.T, s StateStore) {
	ctx := context.Background()
	for _, tk := range []Task{
		confTask("A-1", "p1", "ready"),
		confTask("A-2", "p1", "ready"),
		confTask("B-1", "p2", "ready"),
	} {
		if err := s.CreateTask(ctx, tk); err != nil {
			t.Fatalf("CreateTask %s: %v", tk.ID, err)
		}
	}
	p1, err := s.ListTasks(ctx, "p1")
	if err != nil {
		t.Fatalf("ListTasks p1: %v", err)
	}
	if got := confIDs(p1); len(got) != 2 || got[0] != "A-1" || got[1] != "A-2" {
		t.Fatalf("p1 tasks = %v, want [A-1 A-2]", got)
	}
	p2, err := s.ListTasks(ctx, "p2")
	if err != nil {
		t.Fatalf("ListTasks p2: %v", err)
	}
	if got := confIDs(p2); len(got) != 1 || got[0] != "B-1" {
		t.Fatalf("p2 tasks = %v, want [B-1]", got)
	}
	// Unknown project yields an empty (non-error) list.
	none, err := s.ListTasks(ctx, "nope")
	if err != nil || len(none) != 0 {
		t.Fatalf("ListTasks unknown = %v, err %v", none, err)
	}
}

func confListOrdering(t *testing.T, s StateStore) {
	ctx := context.Background()
	for _, id := range []string{"A-3", "A-1", "A-2"} {
		if err := s.CreateTask(ctx, confTask(id, "p1", "ready")); err != nil {
			t.Fatalf("CreateTask %s: %v", id, err)
		}
	}
	want := []string{"A-1", "A-2", "A-3"}
	for i := 0; i < 3; i++ {
		list, err := s.ListTasks(ctx, "p1")
		if err != nil {
			t.Fatalf("ListTasks: %v", err)
		}
		if got := confIDs(list); len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
			t.Fatalf("call %d: order = %v, want %v", i, got, want)
		}
	}
}

func confSliceRoundTrip(t *testing.T, s StateStore) {
	ctx := context.Background()
	tk := Task{
		ID: "A-1", ProjectID: "p1", Requires: []string{"go", "docker"}, Deps: []string{"PRE-0", "A-0"},
	}
	if err := s.CreateTask(ctx, tk); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	got, err := s.GetTask(ctx, "A-1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if len(got.Requires) != 2 || got.Requires[0] != "go" || got.Requires[1] != "docker" {
		t.Fatalf("Requires round-trip = %v", got.Requires)
	}
	if len(got.Deps) != 2 || got.Deps[0] != "PRE-0" || got.Deps[1] != "A-0" {
		t.Fatalf("Deps round-trip = %v", got.Deps)
	}

	// A task with empty slice fields round-trips without error.
	if err := s.CreateTask(ctx, Task{ID: "A-2", ProjectID: "p1"}); err != nil {
		t.Fatalf("CreateTask empty slices: %v", err)
	}
	got2, err := s.GetTask(ctx, "A-2")
	if err != nil {
		t.Fatalf("GetTask A-2: %v", err)
	}
	if len(got2.Requires) != 0 || len(got2.Deps) != 0 {
		t.Fatalf("empty slices round-trip = %+v", got2)
	}
}

func confLeaseLifecycle(t *testing.T, s StateStore) {
	ctx := context.Background()
	l := Lease{ProjectID: "p1", HostID: "h1", TaskID: "A-1", AcquiredAt: time.Unix(1000, 0).UTC()}
	if err := s.AcquireLease(ctx, l); err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	got, err := s.GetLease(ctx, "p1")
	if err != nil {
		t.Fatalf("GetLease: %v", err)
	}
	if got.HostID != "h1" || got.TaskID != "A-1" || !got.AcquiredAt.Equal(l.AcquiredAt) {
		t.Fatalf("GetLease = %+v, want %+v", got, l)
	}
	leases, err := s.ListLeases(ctx)
	if err != nil || len(leases) != 1 || leases[0].ProjectID != "p1" {
		t.Fatalf("ListLeases = %+v, err %v", leases, err)
	}
	if err := s.ReleaseLease(ctx, "p1"); err != nil {
		t.Fatalf("ReleaseLease: %v", err)
	}
	if _, err := s.GetLease(ctx, "p1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetLease after release: err = %v, want ErrNotFound", err)
	}
}

func confAcquireSecondFails(t *testing.T, s StateStore) {
	ctx := context.Background()
	first := Lease{ProjectID: "p1", HostID: "h1", TaskID: "A-1", AcquiredAt: time.Unix(1000, 0).UTC()}
	if err := s.AcquireLease(ctx, first); err != nil {
		t.Fatalf("first AcquireLease: %v", err)
	}
	second := Lease{ProjectID: "p1", HostID: "h2", TaskID: "A-2", AcquiredAt: time.Unix(2000, 0).UTC()}
	if err := s.AcquireLease(ctx, second); err == nil {
		t.Fatal("second AcquireLease on leased project: want error, got nil")
	}
	// The original lease must be untouched by the failed acquire.
	got, err := s.GetLease(ctx, "p1")
	if err != nil {
		t.Fatalf("GetLease: %v", err)
	}
	if got.TaskID != "A-1" || got.HostID != "h1" {
		t.Fatalf("lease corrupted by failed acquire: %+v", got)
	}
	// Empty project id rejected.
	if err := s.AcquireLease(ctx, Lease{}); err == nil {
		t.Fatal("AcquireLease empty project id: want error, got nil")
	}
}

func confReleaseIdempotent(t *testing.T, s StateStore) {
	ctx := context.Background()
	if err := s.ReleaseLease(ctx, "p1"); err != nil {
		t.Fatalf("release of unheld lease: %v", err)
	}
	if err := s.AcquireLease(ctx, Lease{ProjectID: "p1", HostID: "h1", TaskID: "A-1"}); err != nil {
		t.Fatalf("AcquireLease: %v", err)
	}
	if err := s.ReleaseLease(ctx, "p1"); err != nil {
		t.Fatalf("first release: %v", err)
	}
	if err := s.ReleaseLease(ctx, "p1"); err != nil {
		t.Fatalf("second (idempotent) release: %v", err)
	}
	if err := s.AcquireLease(ctx, Lease{ProjectID: "p1", HostID: "h2", TaskID: "A-2"}); err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
}

// confAcquireConcurrent is the critical atomicity proof: many goroutines race to
// AcquireLease the SAME project at once; exactly one must win and the rest must
// fail with ErrLeaseHeld. For Postgres this exercises the primary-key/ON CONFLICT
// serialisation across concurrent transactions (simulating multiple hosts).
func confAcquireConcurrent(t *testing.T, s StateStore) {
	ctx := context.Background()
	const racers = 32

	var winners int64
	var leaseHeld int64
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	done.Add(racers)

	for i := 0; i < racers; i++ {
		go func(n int) {
			defer done.Done()
			start.Wait() // align all goroutines to fire together
			err := s.AcquireLease(ctx, Lease{
				ProjectID:  "p1",
				HostID:     "host-" + string(rune('A'+n%26)),
				TaskID:     "task",
				AcquiredAt: time.Now().UTC(),
			})
			switch {
			case err == nil:
				atomic.AddInt64(&winners, 1)
			case errors.Is(err, ErrLeaseHeld):
				atomic.AddInt64(&leaseHeld, 1)
			default:
				t.Errorf("unexpected AcquireLease error: %v", err)
			}
		}(i)
	}
	start.Done()
	done.Wait()

	if winners != 1 {
		t.Fatalf("exactly-one-winner violated: winners = %d, want 1 (leaseHeld=%d)", winners, leaseHeld)
	}
	if leaseHeld != racers-1 {
		t.Fatalf("losers = %d, want %d", leaseHeld, racers-1)
	}
	// The single held lease must be present and releasable.
	if _, err := s.GetLease(ctx, "p1"); err != nil {
		t.Fatalf("GetLease after race: %v", err)
	}
	if err := s.ReleaseLease(ctx, "p1"); err != nil {
		t.Fatalf("ReleaseLease after race: %v", err)
	}
}

func confIDs(tasks []Task) []string {
	ids := make([]string, len(tasks))
	for i, tk := range tasks {
		ids[i] = tk.ID
	}
	return ids
}
