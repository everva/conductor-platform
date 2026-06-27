package statestore

import (
	"bytes"
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
		{"HostRegistryUpsert", confHostRegistryUpsert},
		{"HostHeartbeatUpdates", confHostHeartbeat},
		{"GetHostMissingReturnsErrNotFound", confGetHostMissing},
		{"ListHosts", confListHosts},
		{"LeaseLifecycle", confLeaseLifecycle},
		{"AcquireLeaseSecondFails", confAcquireSecondFails},
		{"ReleaseLeaseIdempotent", confReleaseIdempotent},
		{"ReleaseLeaseOwnedFencesByOwner", confReleaseLeaseOwned},
		{"AcquireLeaseConcurrentOneWinner", confAcquireConcurrent},
		{"TaskDiffStoreRoundTrip", confTaskDiffRoundTrip},
		{"CredentialStoreRoundTrip", confCredentialRoundTrip},
		{"EnhanceProgressRoundTrip", confEnhanceProgressRoundTrip},
		{"IntakeSessionRoundTrip", confIntakeSessionRoundTrip},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, cleanup := newStore(t)
			defer cleanup()
			tc.fn(t, s)
		})
	}
}

// confTaskDiffRoundTrip exercises the ADDITIVE TaskDiffStore seam (P2b, ADR-0041): both stores
// implement it (type-asserted from StateStore). It proves put→get round-trip, upsert (a second
// put for the same (project, task) overwrites), per-task scoping, ErrNotFound for a missing pair,
// and ErrInvalid on an empty id — identically across MemoryStore and PostgresStore.
func confTaskDiffRoundTrip(t *testing.T, s StateStore) {
	t.Helper()
	ctx := context.Background()
	tds, ok := s.(TaskDiffStore)
	if !ok {
		t.Fatalf("%T does not implement TaskDiffStore", s)
	}

	// Missing → ErrNotFound (the editor then falls back to the bounded KindDiff patch).
	if _, err := tds.GetTaskDiff(ctx, "p1", "t1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetTaskDiff(missing) err = %v, want ErrNotFound", err)
	}

	// Put → Get round-trip (TaskDiff is all-comparable, so == checks every field).
	d := TaskDiff{ProjectID: "p1", TaskID: "t1", Base: "develop", Branch: "conductor/t1", Patch: "FULL\nPATCH\n", Truncated: true}
	if err := tds.PutTaskDiff(ctx, d); err != nil {
		t.Fatalf("PutTaskDiff: %v", err)
	}
	got, err := tds.GetTaskDiff(ctx, "p1", "t1")
	if err != nil {
		t.Fatalf("GetTaskDiff: %v", err)
	}
	if got != d {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", got, d)
	}

	// Upsert: a second put for the same (project, task) overwrites.
	d2 := d
	d2.Patch = "NEWER\nPATCH\n"
	d2.Truncated = false
	if err := tds.PutTaskDiff(ctx, d2); err != nil {
		t.Fatalf("PutTaskDiff(upsert): %v", err)
	}
	got, err = tds.GetTaskDiff(ctx, "p1", "t1")
	if err != nil {
		t.Fatalf("GetTaskDiff after upsert: %v", err)
	}
	if got.Patch != "NEWER\nPATCH\n" || got.Truncated {
		t.Fatalf("upsert did not overwrite: %+v", got)
	}

	// Scoped by (project, task): a different task is independent.
	if _, err := tds.GetTaskDiff(ctx, "p1", "other"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetTaskDiff(other task) err = %v, want ErrNotFound", err)
	}

	// Empty id → ErrInvalid (guards a malformed write).
	if err := tds.PutTaskDiff(ctx, TaskDiff{ProjectID: "", TaskID: "t1"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("PutTaskDiff(empty project) err = %v, want ErrInvalid", err)
	}
}

// confEnhanceProgressRoundTrip proves the additive EnhanceStore live-progress seam against every
// implementation (Memory + real Postgres via the conformance suite, which exercises migration
// 00011): progress is recorded only while RUNNING, stamps progress_at, and a late ping after
// completion is a no-op that never clobbers the result.
func confEnhanceProgressRoundTrip(t *testing.T, s StateStore) {
	t.Helper()
	ctx := context.Background()
	es, ok := s.(EnhanceStore)
	if !ok {
		t.Fatalf("%T does not implement EnhanceStore", s)
	}

	if err := es.CreateEnhanceJob(ctx, EnhanceJob{ID: "e1", ProjectID: "p1", RoughSpec: "servis şirketini kaldır"}); err != nil {
		t.Fatalf("CreateEnhanceJob: %v", err)
	}

	// Progress on a PENDING (not yet claimed) job is a no-op — only running jobs stream.
	if err := es.UpdateEnhanceProgress(ctx, "e1", "📖 Okunuyor: a.ts"); err != nil {
		t.Fatalf("UpdateEnhanceProgress(pending): %v", err)
	}
	j, err := es.GetEnhanceJob(ctx, "e1")
	if err != nil {
		t.Fatalf("GetEnhanceJob: %v", err)
	}
	if j.Progress != "" || !j.ProgressAt.IsZero() {
		t.Fatalf("pending job must have no progress, got %q at %v", j.Progress, j.ProgressAt)
	}

	// Claim → running, then a progress ping is recorded with a fresh progress_at.
	if _, claimed, cerr := es.ClaimEnhanceJob(ctx, "p1"); cerr != nil || !claimed {
		t.Fatalf("ClaimEnhanceJob: claimed=%v err=%v", claimed, cerr)
	}
	if err := es.UpdateEnhanceProgress(ctx, "e1", "🔎 Aranıyor: serviceCompany"); err != nil {
		t.Fatalf("UpdateEnhanceProgress(running): %v", err)
	}
	j, err = es.GetEnhanceJob(ctx, "e1")
	if err != nil {
		t.Fatalf("GetEnhanceJob: %v", err)
	}
	if j.Progress != "🔎 Aranıyor: serviceCompany" {
		t.Fatalf("progress not recorded: %q", j.Progress)
	}
	if j.ProgressAt.IsZero() {
		t.Fatalf("progress_at must be stamped on a running progress update")
	}

	// Complete → done; a LATE progress ping after completion is a no-op (never clobbers result).
	if err := es.CompleteEnhanceJob(ctx, "e1", "detaylı spec", ""); err != nil {
		t.Fatalf("CompleteEnhanceJob: %v", err)
	}
	if err := es.UpdateEnhanceProgress(ctx, "e1", "📖 Okunuyor: late.ts"); err != nil {
		t.Fatalf("UpdateEnhanceProgress(done): %v", err)
	}
	j, err = es.GetEnhanceJob(ctx, "e1")
	if err != nil {
		t.Fatalf("GetEnhanceJob: %v", err)
	}
	if j.Status != EnhanceDone || j.Result != "detaylı spec" {
		t.Fatalf("completed job must keep its result, got status=%q result=%q", j.Status, j.Result)
	}
	if j.Progress == "📖 Okunuyor: late.ts" {
		t.Fatalf("late progress after completion must be ignored, got %q", j.Progress)
	}
}

// confIntakeSessionRoundTrip proves the additive IntakeSessionStore seam (intake conversation
// history) against every implementation (Memory + real Postgres via the suite, exercising
// migration 00013): upsert by id, list newest-first SCOPED to a project with Messages omitted from
// the light summary, full Get with the thread, upsert PRESERVING created_at while bumping the
// content, and ErrNotFound / ErrInvalid edges — identically across MemoryStore and PostgresStore.
func confIntakeSessionRoundTrip(t *testing.T, s StateStore) {
	t.Helper()
	ctx := context.Background()
	is, ok := s.(IntakeSessionStore)
	if !ok {
		t.Fatalf("%T does not implement IntakeSessionStore", s)
	}

	// Empty id/project is rejected.
	if err := is.PutIntakeSession(ctx, IntakeSession{ID: "", ProjectID: "p1"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("PutIntakeSession(empty id) err = %v, want ErrInvalid", err)
	}
	if err := is.PutIntakeSession(ctx, IntakeSession{ID: "s1", ProjectID: ""}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("PutIntakeSession(empty project) err = %v, want ErrInvalid", err)
	}

	// A missing session is ErrNotFound.
	if _, err := is.GetIntakeSession(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetIntakeSession(missing) err = %v, want ErrNotFound", err)
	}

	// Two conversations in p1 with explicit, distinct created-at so order is deterministic, plus
	// one in p2 that must NOT leak into p1's list.
	t0 := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
	older := IntakeSession{
		ID: "s-old", ProjectID: "p1", Title: "remove service company", CreatedAt: t0,
		Messages: []IntakeMessage{{Role: "you", Text: "kaldır"}, {Role: "assistant", Text: "drafted", Tone: "warn"}},
	}
	newer := IntakeSession{
		ID: "s-new", ProjectID: "p1", Title: "add export", CreatedAt: t0.Add(time.Hour),
		Messages: []IntakeMessage{{Role: "you", Text: "export ekle"}}, Result: "id: E-1\n",
	}
	other := IntakeSession{ID: "s-p2", ProjectID: "p2", Title: "other proj", CreatedAt: t0.Add(2 * time.Hour)}
	for _, sess := range []IntakeSession{older, newer, other} {
		if err := is.PutIntakeSession(ctx, sess); err != nil {
			t.Fatalf("PutIntakeSession(%s): %v", sess.ID, err)
		}
	}

	// List is scoped to p1 and newest-first; summaries omit Messages but keep Result.
	list, err := is.ListIntakeSessions(ctx, "p1")
	if err != nil {
		t.Fatalf("ListIntakeSessions: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListIntakeSessions(p1) len = %d, want 2 (p2 must not leak)", len(list))
	}
	if list[0].ID != "s-new" || list[1].ID != "s-old" {
		t.Fatalf("ListIntakeSessions order = [%s,%s], want [s-new,s-old] (newest first)", list[0].ID, list[1].ID)
	}
	if len(list[0].Messages) != 0 {
		t.Fatalf("list summary must omit Messages, got %d", len(list[0].Messages))
	}
	if list[0].Result != "id: E-1\n" {
		t.Fatalf("list summary must keep Result, got %q", list[0].Result)
	}

	// Get returns the FULL thread.
	full, err := is.GetIntakeSession(ctx, "s-old")
	if err != nil {
		t.Fatalf("GetIntakeSession(s-old): %v", err)
	}
	if len(full.Messages) != 2 || full.Messages[1].Role != "assistant" || full.Messages[1].Tone != "warn" {
		t.Fatalf("GetIntakeSession messages round-trip wrong: %+v", full.Messages)
	}

	// Re-put updates content + bumps UpdatedAt but PRESERVES the original CreatedAt and ProjectID
	// (a caller passing a different project/created-at on update must not move the conversation).
	update := IntakeSession{
		ID: "s-old", ProjectID: "WRONG", Title: "remove service company (v2)", CreatedAt: t0.Add(99 * time.Hour),
		Messages: []IntakeMessage{{Role: "you", Text: "kaldır"}, {Role: "you", Text: "ve test ekle"}},
	}
	if err := is.PutIntakeSession(ctx, update); err != nil {
		t.Fatalf("PutIntakeSession(update): %v", err)
	}
	got, err := is.GetIntakeSession(ctx, "s-old")
	if err != nil {
		t.Fatalf("GetIntakeSession(after update): %v", err)
	}
	if !got.CreatedAt.Equal(t0) {
		t.Fatalf("update must preserve CreatedAt, got %v want %v", got.CreatedAt, t0)
	}
	if got.ProjectID != "p1" {
		t.Fatalf("update must preserve ProjectID, got %q want p1", got.ProjectID)
	}
	if got.Title != "remove service company (v2)" || len(got.Messages) != 2 || got.Messages[1].Text != "ve test ekle" {
		t.Fatalf("update must replace title+messages, got title=%q msgs=%+v", got.Title, got.Messages)
	}
	if !got.UpdatedAt.After(got.CreatedAt) {
		t.Fatalf("UpdatedAt %v must be after CreatedAt %v", got.UpdatedAt, got.CreatedAt)
	}
}

// confCredentialRoundTrip exercises the ADDITIVE CredentialStore seam (L3, ADR-0049): both
// stores implement it (type-asserted from StateStore). It proves put→get round-trip of the
// sealed bytes, upsert (a second put for the same kind overwrites), delete (+ idempotent
// delete of an absent kind), ErrNotFound for a missing/deleted kind, and ErrInvalid on an empty
// kind — identically across MemoryStore and PostgresStore. The store holds only ciphertext +
// nonce (the gateway seals/opens with the master key), so this never deals in plaintext.
func confCredentialRoundTrip(t *testing.T, s StateStore) {
	t.Helper()
	ctx := context.Background()
	cs, ok := s.(CredentialStore)
	if !ok {
		t.Fatalf("%T does not implement CredentialStore", s)
	}

	// Missing → ErrNotFound.
	if _, err := cs.GetCredential(ctx, "claude_oauth"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetCredential(missing) err = %v, want ErrNotFound", err)
	}

	// Put → Get round-trip (byte slices compared field-by-field).
	c := Credential{Kind: "claude_oauth", Ciphertext: []byte{0x01, 0x02, 0x03}, Nonce: []byte{0x09, 0x08}}
	if err := cs.PutCredential(ctx, c); err != nil {
		t.Fatalf("PutCredential: %v", err)
	}
	got, err := cs.GetCredential(ctx, "claude_oauth")
	if err != nil {
		t.Fatalf("GetCredential: %v", err)
	}
	if got.Kind != c.Kind || !bytes.Equal(got.Ciphertext, c.Ciphertext) || !bytes.Equal(got.Nonce, c.Nonce) {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", got, c)
	}

	// Upsert overwrites.
	c2 := Credential{Kind: "claude_oauth", Ciphertext: []byte{0xaa, 0xbb}, Nonce: []byte{0xcc}}
	if err := cs.PutCredential(ctx, c2); err != nil {
		t.Fatalf("PutCredential(upsert): %v", err)
	}
	got, err = cs.GetCredential(ctx, "claude_oauth")
	if err != nil {
		t.Fatalf("GetCredential after upsert: %v", err)
	}
	if !bytes.Equal(got.Ciphertext, c2.Ciphertext) || !bytes.Equal(got.Nonce, c2.Nonce) {
		t.Fatalf("upsert did not overwrite: %+v", got)
	}

	// Delete → ErrNotFound; deleting an absent kind is idempotent (no error).
	if err := cs.DeleteCredential(ctx, "claude_oauth"); err != nil {
		t.Fatalf("DeleteCredential: %v", err)
	}
	if _, err := cs.GetCredential(ctx, "claude_oauth"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetCredential after delete err = %v, want ErrNotFound", err)
	}
	if err := cs.DeleteCredential(ctx, "claude_oauth"); err != nil {
		t.Fatalf("DeleteCredential(absent) must be idempotent, got %v", err)
	}

	// Empty kind → ErrInvalid.
	if err := cs.PutCredential(ctx, Credential{Kind: "", Ciphertext: []byte{0x01}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("PutCredential(empty kind) err = %v, want ErrInvalid", err)
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
	// A freshly created task defaults to not-aborting (F-2 additive field) and
	// not-approved (Faz-1.5-b additive field).
	if got.AbortRequested {
		t.Fatalf("fresh task must default AbortRequested=false, got %+v", got)
	}
	if got.Approved {
		t.Fatalf("fresh task must default Approved=false, got %+v", got)
	}
	// A freshly created task defaults to an empty LastError (00012 additive column).
	if got.LastError != "" {
		t.Fatalf("fresh task must default LastError=\"\", got %q", got.LastError)
	}
	got.Status = "done"
	got.RetryCount = 2
	got.AbortRequested = true                             // F-2: round-trip the additive abort signal.
	got.Approved = true                                   // Faz-1.5-b: round-trip the additive approval signal.
	got.LastError = "agent run failed: malformed verdict" // 00012: round-trip the additive block reason.
	if err := s.UpdateTask(ctx, got); err != nil {
		t.Fatalf("UpdateTask: %v", err)
	}
	reread, err := s.GetTask(ctx, "A-1")
	if err != nil {
		t.Fatalf("GetTask after update: %v", err)
	}
	if reread.Status != "done" || reread.RetryCount != 2 || !reread.AbortRequested || !reread.Approved {
		t.Fatalf("after update = %+v, want status=done retry=2 abort=true approved=true", reread)
	}
	if reread.LastError != "agent run failed: malformed verdict" {
		t.Fatalf("LastError round-trip = %q, want the persisted block reason", reread.LastError)
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

// confHostRegistryUpsert proves RegisterHost inserts a new host and then upserts
// (overwrites capabilities + heartbeat) on a second call with the same ID, in
// both stores (ADR-0024 self-registration). It also proves a zero LastHeartbeat
// is stamped live on registration and an empty id is rejected.
func confHostRegistryUpsert(t *testing.T, s StateStore) {
	ctx := context.Background()

	// Insert: a fresh host with capabilities and a zero heartbeat is stamped live.
	if err := s.RegisterHost(ctx, Host{ID: "mac-1", Capabilities: []string{"ios-build", "macos", "web"}}); err != nil {
		t.Fatalf("RegisterHost insert: %v", err)
	}
	got, err := s.GetHost(ctx, "mac-1")
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if len(got.Capabilities) != 3 || got.Capabilities[0] != "ios-build" || got.Capabilities[2] != "web" {
		t.Fatalf("capabilities round-trip = %v", got.Capabilities)
	}
	if got.LastHeartbeat.IsZero() {
		t.Fatalf("zero LastHeartbeat must be stamped live on register, got zero")
	}

	// Upsert: re-register the SAME id with different capabilities + an explicit
	// heartbeat. capabilities and heartbeat are overwritten, not duplicated.
	hb := time.Unix(5000, 0).UTC()
	if err := s.RegisterHost(ctx, Host{ID: "mac-1", Capabilities: []string{"linux", "backend"}, LastHeartbeat: hb}); err != nil {
		t.Fatalf("RegisterHost upsert: %v", err)
	}
	got, err = s.GetHost(ctx, "mac-1")
	if err != nil {
		t.Fatalf("GetHost after upsert: %v", err)
	}
	if len(got.Capabilities) != 2 || got.Capabilities[0] != "linux" || got.Capabilities[1] != "backend" {
		t.Fatalf("upsert did not overwrite capabilities: %v", got.Capabilities)
	}
	if !got.LastHeartbeat.Equal(hb) {
		t.Fatalf("upsert heartbeat = %v, want %v", got.LastHeartbeat, hb)
	}
	// Exactly one row for the id (upsert, not insert).
	list, err := s.ListHosts(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListHosts after upsert = %+v, err %v, want 1 host", list, err)
	}

	// A host with NO capabilities registers fine (backward compatible: matches
	// only no-requires lanes; routing is 2B-2).
	if err := s.RegisterHost(ctx, Host{ID: "bare"}); err != nil {
		t.Fatalf("RegisterHost no-capabilities: %v", err)
	}
	bare, err := s.GetHost(ctx, "bare")
	if err != nil {
		t.Fatalf("GetHost bare: %v", err)
	}
	if len(bare.Capabilities) != 0 {
		t.Fatalf("bare host capabilities = %v, want empty", bare.Capabilities)
	}

	// Empty id rejected.
	if err := s.RegisterHost(ctx, Host{}); err == nil {
		t.Fatal("RegisterHost empty id: want error, got nil")
	}
}

// confHostHeartbeat proves HostHeartbeat advances LastHeartbeat on a registered
// host and is ErrNotFound for an unknown host (no silent insert), in both stores.
func confHostHeartbeat(t *testing.T, s StateStore) {
	ctx := context.Background()
	if err := s.RegisterHost(ctx, Host{ID: "h1", Capabilities: []string{"linux"}, LastHeartbeat: time.Unix(1000, 0).UTC()}); err != nil {
		t.Fatalf("RegisterHost: %v", err)
	}
	beat := time.Unix(9000, 0).UTC()
	if err := s.HostHeartbeat(ctx, "h1", beat); err != nil {
		t.Fatalf("HostHeartbeat: %v", err)
	}
	got, err := s.GetHost(ctx, "h1")
	if err != nil {
		t.Fatalf("GetHost: %v", err)
	}
	if !got.LastHeartbeat.Equal(beat) {
		t.Fatalf("LastHeartbeat = %v, want %v", got.LastHeartbeat, beat)
	}
	// Capabilities untouched by a heartbeat.
	if len(got.Capabilities) != 1 || got.Capabilities[0] != "linux" {
		t.Fatalf("heartbeat altered capabilities: %v", got.Capabilities)
	}
	// Heartbeat on an unknown host is ErrNotFound.
	if err := s.HostHeartbeat(ctx, "ghost", beat); !errors.Is(err, ErrNotFound) {
		t.Fatalf("HostHeartbeat unknown: err = %v, want ErrNotFound", err)
	}
}

// confGetHostMissing proves GetHost on an unknown id is ErrNotFound in both stores.
func confGetHostMissing(t *testing.T, s StateStore) {
	ctx := context.Background()
	if _, err := s.GetHost(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetHost missing: err = %v, want ErrNotFound", err)
	}
}

// confListHosts proves ListHosts returns all registered hosts ordered by ID and
// an empty (non-error) list when none are registered.
func confListHosts(t *testing.T, s StateStore) {
	ctx := context.Background()
	none, err := s.ListHosts(ctx)
	if err != nil || len(none) != 0 {
		t.Fatalf("ListHosts empty = %+v, err %v", none, err)
	}
	for _, id := range []string{"h3", "h1", "h2"} {
		if err := s.RegisterHost(ctx, Host{ID: id, Capabilities: []string{"linux"}}); err != nil {
			t.Fatalf("RegisterHost %s: %v", id, err)
		}
	}
	list, err := s.ListHosts(ctx)
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	if len(list) != 3 || list[0].ID != "h1" || list[1].ID != "h2" || list[2].ID != "h3" {
		t.Fatalf("ListHosts order = %+v, want [h1 h2 h3]", list)
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

// confReleaseLeaseOwned is the C-2 proof: ReleaseLeaseOwned deletes ONLY the
// matching owner's lease. The scenario mirrors a false-reap → re-acquire: host A
// held the lease, the reaper released it, host B re-acquired the SAME project — and
// then A's owner-scoped release must NOT delete B's lease. It also proves the
// no-op-when-absent and no-op-when-different-task idempotency.
func confReleaseLeaseOwned(t *testing.T, s StateStore) {
	ctx := context.Background()

	// Idempotent when no lease is held at all.
	if err := s.ReleaseLeaseOwned(ctx, "p1", "host-A", "A-1"); err != nil {
		t.Fatalf("ReleaseLeaseOwned with no lease held: %v", err)
	}

	// Host A holds the lease; releasing as A (the owner) deletes it.
	if err := s.AcquireLease(ctx, Lease{ProjectID: "p1", HostID: "host-A", TaskID: "A-1"}); err != nil {
		t.Fatalf("AcquireLease host-A: %v", err)
	}
	if err := s.ReleaseLeaseOwned(ctx, "p1", "host-A", "A-1"); err != nil {
		t.Fatalf("ReleaseLeaseOwned by owner: %v", err)
	}
	if _, err := s.GetLease(ctx, "p1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("owner release should have deleted lease; GetLease err = %v, want ErrNotFound", err)
	}

	// The C-2 scenario: host B now holds the lease (after a reap+re-acquire); host
	// A's stale owner-scoped release must NOT delete B's lease.
	if err := s.AcquireLease(ctx, Lease{ProjectID: "p1", HostID: "host-B", TaskID: "B-1"}); err != nil {
		t.Fatalf("AcquireLease host-B: %v", err)
	}
	if err := s.ReleaseLeaseOwned(ctx, "p1", "host-A", "A-1"); err != nil {
		t.Fatalf("ReleaseLeaseOwned by non-owner A: %v", err)
	}
	got, err := s.GetLease(ctx, "p1")
	if err != nil {
		t.Fatalf("B's lease must survive A's release; GetLease: %v", err)
	}
	if got.HostID != "host-B" || got.TaskID != "B-1" {
		t.Fatalf("B's lease was clobbered by A's owner-scoped release: %+v (C-2 regression)", got)
	}

	// Same host, WRONG task is also a no-op (owner fencing is on host AND task).
	if err := s.ReleaseLeaseOwned(ctx, "p1", "host-B", "WRONG-TASK"); err != nil {
		t.Fatalf("ReleaseLeaseOwned wrong task: %v", err)
	}
	if _, err := s.GetLease(ctx, "p1"); err != nil {
		t.Fatalf("wrong-task release must not delete; GetLease: %v", err)
	}

	// Finally, the true owner B releases its own lease cleanly.
	if err := s.ReleaseLeaseOwned(ctx, "p1", "host-B", "B-1"); err != nil {
		t.Fatalf("ReleaseLeaseOwned by owner B: %v", err)
	}
	if _, err := s.GetLease(ctx, "p1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("B's own release should delete; GetLease err = %v, want ErrNotFound", err)
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
