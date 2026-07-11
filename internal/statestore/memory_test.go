package statestore

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

// memTask is a small helper producing a populated Task for tests.
func memTask(id, projectID, status string) Task {
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

// TestMemoryStore_ImplementsContract is a compile-time guard that the concrete
// store satisfies the frozen StateStore interface.
func TestMemoryStore_ImplementsContract(t *testing.T) {
	var _ StateStore = NewMemoryStore()
}

// TestMemoryStore_TaskCRUD covers create -> get -> update status -> list,
// confirming a status change is reflected with no stale read.
func TestMemoryStore_TaskCRUD(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

	if err := s.CreateTask(ctx, memTask("A-1", "p1", "ready")); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	got, err := s.GetTask(ctx, "A-1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != "ready" {
		t.Fatalf("status = %q, want ready", got.Status)
	}

	got.Status = "done"
	if err := s.UpdateTask(ctx, got); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}

	reread, err := s.GetTask(ctx, "A-1")
	if err != nil {
		t.Fatalf("GetTask after update: %v", err)
	}
	if reread.Status != "done" {
		t.Fatalf("status after update = %q, want done (stale read)", reread.Status)
	}

	list, err := s.ListTasks(ctx, "p1")
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(list) != 1 || list[0].Status != "done" {
		t.Fatalf("list = %+v, want one task with status done", list)
	}
}

// TestMemoryStore_GetMissing_ReturnsErrNotFound checks every lookup verb returns
// the frozen ErrNotFound sentinel (detectable via errors.Is) for a missing id.
func TestMemoryStore_GetMissing_ReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

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
	if err := s.UpdateTask(ctx, memTask("ghost", "p1", "ready")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateTask missing: err = %v, want ErrNotFound", err)
	}
}

// TestMemoryStore_AcquireLease_SecondAcquireFails verifies the single-host lease
// is atomic: a second acquire on an already-leased project fails.
func TestMemoryStore_AcquireLease_SecondAcquireFails(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

	first := Lease{ProjectID: "p1", HostID: "h1", TaskID: "A-1", AcquiredAt: time.Unix(1000, 0)}
	if err := s.AcquireLease(ctx, first); err != nil {
		t.Fatalf("first AcquireLease: %v", err)
	}

	second := Lease{ProjectID: "p1", HostID: "h2", TaskID: "A-2", AcquiredAt: time.Unix(2000, 0)}
	if err := s.AcquireLease(ctx, second); err == nil {
		t.Fatal("second AcquireLease on leased project: want error, got nil (double-lease)")
	}

	// The original lease must be untouched by the failed acquire.
	got, err := s.GetLease(ctx, "p1")
	if err != nil {
		t.Fatalf("GetLease: %v", err)
	}
	if got.TaskID != "A-1" || got.HostID != "h1" {
		t.Fatalf("lease corrupted by failed acquire: %+v", got)
	}
}

// TestMemoryStore_ReleaseLease_Idempotent confirms releasing is idempotent: a
// release of an unheld lease (and a double release) returns no error, and after
// release the project can be leased again.
func TestMemoryStore_ReleaseLease_Idempotent(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

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

	// Released project must be re-acquirable.
	if err := s.AcquireLease(ctx, Lease{ProjectID: "p1", HostID: "h2", TaskID: "A-2"}); err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
}

// TestMemoryStore_ReturnedValueIsCopy ensures stored values are copies: mutating
// a returned struct (including its slices) does not corrupt the store.
func TestMemoryStore_ReturnedValueIsCopy(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

	orig := memTask("A-1", "p1", "ready")
	if err := s.CreateTask(ctx, orig); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Mutating the struct passed to CreateTask must not affect the store.
	orig.Status = "tampered"
	orig.Deps[0] = "tampered"

	got, err := s.GetTask(ctx, "A-1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != "ready" || got.Deps[0] != "PRE-0" {
		t.Fatalf("store mutated via input alias: %+v", got)
	}

	// Mutating a returned value must not affect the store either.
	got.Status = "tampered"
	got.Deps[0] = "tampered"
	got.Requires[0] = "tampered"

	again, err := s.GetTask(ctx, "A-1")
	if err != nil {
		t.Fatalf("GetTask again: %v", err)
	}
	if again.Status != "ready" || again.Deps[0] != "PRE-0" || again.Requires[0] != "go" {
		t.Fatalf("store mutated via returned alias: %+v", again)
	}
}

// TestMemoryStore_ListOrderingStable checks list results are deterministically
// ordered (by ID) regardless of insertion order, across repeated calls.
func TestMemoryStore_ListOrderingStable(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

	for _, id := range []string{"A-3", "A-1", "A-2"} {
		if err := s.CreateTask(ctx, memTask(id, "p1", "ready")); err != nil {
			t.Fatalf("CreateTask %s: %v", id, err)
		}
	}

	want := []string{"A-1", "A-2", "A-3"}
	for i := 0; i < 5; i++ {
		list, err := s.ListTasks(ctx, "p1")
		if err != nil {
			t.Fatalf("ListTasks: %v", err)
		}
		if len(list) != len(want) {
			t.Fatalf("len = %d, want %d", len(list), len(want))
		}
		for j, id := range want {
			if list[j].ID != id {
				t.Fatalf("call %d: order = %v, want %v", i, idsOf(list), want)
			}
		}
	}
}

// TestMemoryStore_ListTasksScopedByProject confirms ListTasks filters by project.
func TestMemoryStore_ListTasksScopedByProject(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

	mustCreateTask(t, s, memTask("A-1", "p1", "ready"))
	mustCreateTask(t, s, memTask("A-2", "p1", "ready"))
	mustCreateTask(t, s, memTask("B-1", "p2", "ready"))

	p1, err := s.ListTasks(ctx, "p1")
	if err != nil {
		t.Fatalf("ListTasks p1: %v", err)
	}
	if got := idsOf(p1); len(got) != 2 || got[0] != "A-1" || got[1] != "A-2" {
		t.Fatalf("p1 tasks = %v, want [A-1 A-2]", got)
	}

	p2, err := s.ListTasks(ctx, "p2")
	if err != nil {
		t.Fatalf("ListTasks p2: %v", err)
	}
	if got := idsOf(p2); len(got) != 1 || got[0] != "B-1" {
		t.Fatalf("p2 tasks = %v, want [B-1]", got)
	}
}

// TestMemoryStore_ProjectAndScenarioCRUD exercises the project and scenario verbs.
func TestMemoryStore_ProjectAndScenarioCRUD(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

	if err := s.CreateProject(ctx, Project{ID: "p1", Repo: "owner/name", BaseBranch: "develop"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	p, err := s.GetProject(ctx, "p1")
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if p.Repo != "owner/name" {
		t.Fatalf("repo = %q", p.Repo)
	}
	projects, err := s.ListProjects(ctx)
	if err != nil || len(projects) != 1 {
		t.Fatalf("ListProjects = %+v, err %v", projects, err)
	}

	sc := Scenario{ID: "S-1", ProjectID: "p1", Title: "t", Acceptance: []string{"crit"}, Deps: []string{"PRE-0"}}
	if err := s.CreateScenario(ctx, sc); err != nil {
		t.Fatalf("CreateScenario: %v", err)
	}
	gotSc, err := s.GetScenario(ctx, "S-1")
	if err != nil {
		t.Fatalf("GetScenario: %v", err)
	}
	// Mutating the returned scenario slices must not corrupt the store.
	gotSc.Acceptance[0] = "tampered"
	reread, err := s.GetScenario(ctx, "S-1")
	if err != nil {
		t.Fatalf("GetScenario reread: %v", err)
	}
	if reread.Acceptance[0] != "crit" {
		t.Fatalf("scenario slice not copied: %+v", reread)
	}

	scenarios, err := s.ListScenarios(ctx, "p1")
	if err != nil || len(scenarios) != 1 {
		t.Fatalf("ListScenarios = %+v, err %v", scenarios, err)
	}
}

// TestMemoryStore_DuplicateCreateFails confirms create verbs reject a duplicate id
// rather than silently overwriting.
func TestMemoryStore_DuplicateCreateFails(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

	mustCreateTask(t, s, memTask("A-1", "p1", "ready"))
	if err := s.CreateTask(ctx, memTask("A-1", "p1", "running")); err == nil {
		t.Fatal("duplicate CreateTask: want error, got nil")
	}
	// Original must be untouched.
	got, err := s.GetTask(ctx, "A-1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != "ready" {
		t.Fatalf("duplicate create overwrote store: %+v", got)
	}
}

// TestMemoryStore_Concurrent drives parallel writers and readers; run under
// `go test -race` it asserts the store is free of data races.
func TestMemoryStore_Concurrent(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	mustCreateTask(t, s, memTask("A-1", "p1", "ready"))

	const workers = 16
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_, _ = s.GetTask(ctx, "A-1")
				_, _ = s.ListTasks(ctx, "p1")
				task := memTask("A-1", "p1", "running")
				_ = s.UpdateTask(ctx, task)
				lease := Lease{ProjectID: "p1", HostID: "h", TaskID: "A-1"}
				if s.AcquireLease(ctx, lease) == nil {
					_ = s.ReleaseLease(ctx, "p1")
				}
			}
		}(i)
	}
	wg.Wait()
}

func mustCreateTask(t *testing.T, s *MemoryStore, task Task) {
	t.Helper()
	if err := s.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("CreateTask %s: %v", task.ID, err)
	}
}

func idsOf(tasks []Task) []string {
	ids := make([]string, len(tasks))
	for i, task := range tasks {
		ids[i] = task.ID
	}
	return ids
}

// TestMemoryStore_ReplaceScenarioFindings covers the review-findings-persistence path: new criteria
// show up, duplicates (existing OR within the batch) are de-duplicated, blanks are dropped, and a
// missing scenario returns ErrNotFound. This is the seam that lets a dense screen converge across
// autoheal retries (the findings survive the fresh worktree via the scenario).
func TestMemoryStore_ReplaceScenarioFindings(t *testing.T) {
	ctx := context.Background()
	const pfx = "FINDING: "
	s := NewMemoryStore()
	if err := s.CreateScenario(ctx, Scenario{ID: "S-1", ProjectID: "p1", Title: "t", Acceptance: []string{"a"}}); err != nil {
		t.Fatalf("CreateScenario: %v", err)
	}
	// two new, one dup-within-batch, one blank
	if err := s.ReplaceScenarioFindings(ctx, "S-1", pfx, []string{pfx + "b", " " + pfx + "c ", pfx + "c", "  "}); err != nil {
		t.Fatalf("ReplaceScenarioFindings: %v", err)
	}
	got, err := s.GetScenario(ctx, "S-1")
	if err != nil {
		t.Fatalf("GetScenario: %v", err)
	}
	if want := []string{"a", pfx + "b", pfx + "c"}; !slices.Equal(got.Acceptance, want) {
		t.Fatalf("acceptance = %v, want %v", got.Acceptance, want)
	}
	// missing scenario -> ErrNotFound
	if err := s.ReplaceScenarioFindings(ctx, "nope", pfx, []string{pfx + "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing scenario must be ErrNotFound, got %v", err)
	}
}

// A finding persisted by an EARLIER gate run must be SUPERSEDED by the next run's findings, never
// carried alongside them. Appending is what let a single bad finding — the gate reporting its own
// banner, "verify: node v22.23.0 / npm 10.9.8" — stick to 30 scenarios permanently, ordering the
// developer to "resolve" a version string on every future attempt. The scenario's OWN spec lines
// must survive untouched; only the findings block turns over. Clearing (empty criteria) is what a
// now-passing gate leaves behind.
func TestMemoryStore_ReplaceScenarioFindings_SupersedesTheEarlierRound(t *testing.T) {
	ctx := context.Background()
	const pfx = "Resolve this prior-review finding before merge: "
	s := NewMemoryStore()
	spec := []string{"DEFECT: order-print.ts hardcodes a 19% tax", "FIX: read the real tax field"}
	if err := s.CreateScenario(ctx, Scenario{ID: "S-1", ProjectID: "p1", Title: "t", Acceptance: spec}); err != nil {
		t.Fatalf("CreateScenario: %v", err)
	}
	// Round 1 persists the USELESS banner finding.
	poison := pfx + `gate "build" failed: verify: node v22.23.0 / npm 10.9.8`
	if err := s.ReplaceScenarioFindings(ctx, "S-1", pfx, []string{poison}); err != nil {
		t.Fatalf("round 1: %v", err)
	}
	// Round 2 reports the REAL error. The banner line must be gone, not accumulated beside it.
	real := pfx + `gate "build" failed: error TS2551: Property 'tax' does not exist`
	if err := s.ReplaceScenarioFindings(ctx, "S-1", pfx, []string{real}); err != nil {
		t.Fatalf("round 2: %v", err)
	}
	got, err := s.GetScenario(ctx, "S-1")
	if err != nil {
		t.Fatalf("GetScenario: %v", err)
	}
	if slices.Contains(got.Acceptance, poison) {
		t.Fatalf("the superseded banner finding is still poisoning the acceptance: %v", got.Acceptance)
	}
	if want := append(slices.Clone(spec), real); !slices.Equal(got.Acceptance, want) {
		t.Fatalf("acceptance = %v, want %v (spec preserved, findings turned over)", got.Acceptance, want)
	}
	// A now-green gate reports no findings: the block CLEARS, leaving only the scenario's spec.
	if err := s.ReplaceScenarioFindings(ctx, "S-1", pfx, nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, err = s.GetScenario(ctx, "S-1")
	if err != nil {
		t.Fatalf("GetScenario: %v", err)
	}
	if !slices.Equal(got.Acceptance, spec) {
		t.Fatalf("acceptance = %v, want the bare spec %v", got.Acceptance, spec)
	}
}
