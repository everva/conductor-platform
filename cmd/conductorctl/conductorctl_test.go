package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/everva/conductor-platform/internal/registry"
	"github.com/everva/conductor-platform/internal/statestore"
)

func newApp(t *testing.T) (*app, *statestore.MemoryStore, *MemoryController, *bytes.Buffer) {
	t.Helper()
	store := statestore.NewMemoryStore()
	ctrl := NewMemoryController()
	var out bytes.Buffer
	return &app{store: store, ctrl: ctrl, out: &out}, store, ctrl, &out
}

func writeScenario(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "scn.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write scenario: %v", err)
	}
	return path
}

func TestConductorctl_Onboard_CreatesProject_Idempotent(t *testing.T) {
	ctx := context.Background()
	a, store, _, _ := newApp(t)

	p1, err := a.onboard(ctx, "owner/repo", "")
	if err != nil {
		t.Fatalf("onboard: %v", err)
	}
	if p1.Repo != "owner/repo" || p1.BaseBranch != defaultBaseBranch {
		t.Fatalf("unexpected project: %+v", p1)
	}

	// Re-onboard the same repo: no duplicate, returns the existing project.
	p2, err := a.onboard(ctx, "owner/repo", "main")
	if err != nil {
		t.Fatalf("re-onboard: %v", err)
	}
	if p2.ID != p1.ID || p2.BaseBranch != p1.BaseBranch {
		t.Fatalf("re-onboard should return existing project unchanged: %+v vs %+v", p2, p1)
	}
	projects, err := store.ListProjects(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("expected 1 project after idempotent re-onboard, got %d", len(projects))
	}
}

// richScenario renders a valid rich (ADR-0012/ADR-0018) scenario document: the
// intake path now delegates to internal/intake, which requires title, acceptance,
// and a repo-EXTERNAL hidden_holdout_ref (a `store://...` locator here) in addition
// to id/lane/tier. deps is rendered as a YAML flow list.
func richScenario(id, lane, tier string, deps ...string) string {
	depList := "[" + strings.Join(quoteAll(deps), ", ") + "]"
	return strings.Join([]string{
		"id: " + id,
		"title: " + strconv.Quote(id+" rich scenario"),
		"lane: " + lane,
		"tier: " + tier,
		"deps: " + depList,
		"acceptance:",
		"  - " + strconv.Quote("gate is non-empty for "+id),
		"hidden_holdout_ref: " + strconv.Quote("store://holdouts/"+id+"/spec.yaml"),
		"",
	}, "\n")
}

func quoteAll(ss []string) []string {
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		out = append(out, strconv.Quote(s))
	}
	return out
}

func TestConductorctl_Intake_CreatesTaskAndScenario(t *testing.T) {
	ctx := context.Background()
	a, store, _, _ := newApp(t)
	if _, err := a.onboard(ctx, "owner/repo", ""); err != nil {
		t.Fatalf("onboard: %v", err)
	}

	path := writeScenario(t, richScenario("A-1", "statestore", "T1"))
	res, err := a.intake(ctx, "repo", path)
	if err != nil {
		t.Fatalf("intake: %v", err)
	}
	if len(res.Created) != 1 || res.Created[0] != "A-1" || len(res.Skipped) != 0 {
		t.Fatalf("unexpected intake result: %+v", res)
	}
	// Both persisted in the shared store with the rich fields projected down.
	scn, err := store.GetScenario(ctx, "A-1")
	if err != nil {
		t.Fatalf("scenario not persisted: %v", err)
	}
	if scn.Lane != "statestore" || scn.Tier != "T1" || scn.Title == "" || len(scn.Acceptance) == 0 || scn.HoldoutRef == "" {
		t.Fatalf("rich fields not projected onto scenario: %+v", scn)
	}
	task, err := store.GetTask(ctx, "A-1")
	if err != nil {
		t.Fatalf("task not persisted: %v", err)
	}
	if task.Status != registry.StatusTodo || task.ScenarioID != "A-1" {
		t.Fatalf("unexpected task: %+v", task)
	}

	// Idempotent re-intake of the same file is a no-op: skipped, not duplicated.
	res2, err := a.intake(ctx, "repo", path)
	if err != nil {
		t.Fatalf("re-intake: %v", err)
	}
	if len(res2.Created) != 0 || len(res2.Skipped) != 1 || res2.Skipped[0] != "A-1" {
		t.Fatalf("re-intake should skip already-present scenario: %+v", res2)
	}
}

func TestConductorctl_Intake_KnownDep_OK(t *testing.T) {
	ctx := context.Background()
	a, store, _, _ := newApp(t)
	if _, err := a.onboard(ctx, "owner/repo", ""); err != nil {
		t.Fatalf("onboard: %v", err)
	}
	// First task with no deps.
	p1 := writeScenario(t, richScenario("A-1", "x", "T1"))
	if _, err := a.intake(ctx, "repo", p1); err != nil {
		t.Fatalf("intake A-1: %v", err)
	}
	// Second task depends on the existing A-1: accepted.
	p2 := writeScenario(t, richScenario("A-2", "x", "T1", "A-1"))
	if _, err := a.intake(ctx, "repo", p2); err != nil {
		t.Fatalf("intake A-2 with known dep: %v", err)
	}
	task, err := store.GetTask(ctx, "A-2")
	if err != nil {
		t.Fatalf("A-2 not persisted: %v", err)
	}
	if len(task.Deps) != 1 || task.Deps[0] != "A-1" {
		t.Fatalf("deps not carried: %+v", task.Deps)
	}
}

func TestConductorctl_Intake_UnknownDep_Errors(t *testing.T) {
	ctx := context.Background()
	a, store, _, _ := newApp(t)
	if _, err := a.onboard(ctx, "owner/repo", ""); err != nil {
		t.Fatalf("onboard: %v", err)
	}
	path := writeScenario(t, richScenario("B-1", "x", "T2", "DOES-NOT-EXIST"))
	if _, err := a.intake(ctx, "repo", path); err == nil {
		t.Fatalf("expected error for unknown dep")
	}
	// No partial write: neither the scenario nor the task should exist.
	if _, gerr := store.GetScenario(ctx, "B-1"); gerr == nil {
		t.Fatalf("scenario must not be written on unknown-dep refusal")
	}
	if _, gerr := store.GetTask(ctx, "B-1"); gerr == nil {
		t.Fatalf("task must not be written on unknown-dep refusal")
	}
}

func TestConductorctl_Intake_MissingField_NoPartialWrite(t *testing.T) {
	ctx := context.Background()
	a, store, _, _ := newApp(t)
	if _, err := a.onboard(ctx, "owner/repo", ""); err != nil {
		t.Fatalf("onboard: %v", err)
	}
	// Missing required title/lane/tier/acceptance/holdout: rich validation rejects.
	path := writeScenario(t, "id: C-9\ntitle: broken\n")
	if _, err := a.intake(ctx, "repo", path); err == nil {
		t.Fatalf("expected error for missing fields")
	}
	if _, gerr := store.GetScenario(ctx, "C-9"); gerr == nil {
		t.Fatalf("no partial write: scenario must not exist")
	}
}

func TestConductorctl_Intake_RepoInternalHoldout_Rejected(t *testing.T) {
	ctx := context.Background()
	a, store, _, _ := newApp(t)
	if _, err := a.onboard(ctx, "owner/repo", ""); err != nil {
		t.Fatalf("onboard: %v", err)
	}
	// A repo-relative holdout path violates ADR-0018; rich validation rejects it.
	body := strings.Join([]string{
		"id: D-1",
		`title: "holdout in repo"`,
		"lane: x",
		"tier: T1",
		"acceptance:",
		`  - "criterion"`,
		`hidden_holdout_ref: "testdata/holdouts/D-1.yaml"`,
		"",
	}, "\n")
	path := writeScenario(t, body)
	if _, err := a.intake(ctx, "repo", path); err == nil {
		t.Fatalf("expected error for repo-internal holdout ref")
	}
	if _, gerr := store.GetScenario(ctx, "D-1"); gerr == nil {
		t.Fatalf("no partial write: scenario must not exist")
	}
}

func TestConductorctl_Intake_UnknownProject_Errors(t *testing.T) {
	ctx := context.Background()
	a, _, _, _ := newApp(t)
	// No onboard: the project does not exist; internal/intake must error clearly.
	path := writeScenario(t, richScenario("A-1", "x", "T1"))
	if _, err := a.intake(ctx, "nope", path); err == nil {
		t.Fatalf("expected error for unknown project")
	}
}

func TestConductorctl_Status_RendersLedger(t *testing.T) {
	ctx := context.Background()
	a, store, _, out := newApp(t)
	if _, err := a.onboard(ctx, "owner/repo", ""); err != nil {
		t.Fatalf("onboard: %v", err)
	}
	// Insert out of id order to prove stable ordering on output.
	for _, id := range []string{"A-3", "A-1", "A-2"} {
		if err := store.CreateTask(ctx, statestore.Task{ID: id, ProjectID: "repo", Lane: "x", Tier: "T1", Status: registry.StatusTodo}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	if err := store.AcquireLease(ctx, statestore.Lease{ProjectID: "repo", HostID: "host-1", TaskID: "A-2"}); err != nil {
		t.Fatalf("lease: %v", err)
	}

	if err := a.status(ctx, "repo", false); err != nil {
		t.Fatalf("status: %v", err)
	}
	text := out.String()
	i1, i2, i3 := strings.Index(text, "A-1"), strings.Index(text, "A-2"), strings.Index(text, "A-3")
	if i1 < 0 || i1 >= i2 || i2 >= i3 {
		t.Fatalf("tasks not in stable id order:\n%s", text)
	}
	if !strings.Contains(text, "host-1") {
		t.Fatalf("lease holder not rendered:\n%s", text)
	}

	// --json round-trips the same ledger.
	out.Reset()
	if err := a.status(ctx, "repo", true); err != nil {
		t.Fatalf("status json: %v", err)
	}
	var decoded struct {
		Project string `json:"project"`
		Lease   string `json:"lease"`
		Tasks   []struct {
			ID    string `json:"id"`
			Lease string `json:"lease"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("json decode: %v\n%s", err, out.String())
	}
	if len(decoded.Tasks) != 3 || decoded.Tasks[0].ID != "A-1" {
		t.Fatalf("json tasks wrong: %+v", decoded.Tasks)
	}
	if decoded.Lease != "A-2" {
		t.Fatalf("json lease task = %q, want A-2", decoded.Lease)
	}
}

func TestConductorctl_PauseResume_TogglesRunState(t *testing.T) {
	ctx := context.Background()
	a, _, ctrl, _ := newApp(t)

	if paused, _ := ctrl.Paused(ctx, "repo"); paused {
		t.Fatalf("project should start unpaused")
	}
	if err := a.pause(ctx, "repo"); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if paused, _ := ctrl.Paused(ctx, "repo"); !paused {
		t.Fatalf("pause should make next tick a no-op (paused)")
	}
	// Idempotent: a double-pause stays paused, no flip.
	if err := a.pause(ctx, "repo"); err != nil {
		t.Fatalf("double pause: %v", err)
	}
	if paused, _ := ctrl.Paused(ctx, "repo"); !paused {
		t.Fatalf("double pause must stay paused")
	}
	// Resume restores; idempotent.
	if err := a.resume(ctx, "repo"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if paused, _ := ctrl.Paused(ctx, "repo"); paused {
		t.Fatalf("resume should restore run state")
	}
	if err := a.resume(ctx, "repo"); err != nil {
		t.Fatalf("double resume: %v", err)
	}
	if paused, _ := ctrl.Paused(ctx, "repo"); paused {
		t.Fatalf("double resume must stay running")
	}
}

// --- dispatcher / exit-code tests -------------------------------------------

func TestRun_UnknownSubcommand_NonZero(t *testing.T) {
	a, _, _, _ := newApp(t)
	var stderr bytes.Buffer
	code := run(context.Background(), a, []string{"bogus"}, &stderr)
	if code == 0 {
		t.Fatalf("unknown subcommand must exit non-zero")
	}
	if !strings.Contains(stderr.String(), "unknown subcommand") {
		t.Fatalf("expected clear message, got %q", stderr.String())
	}
}

func TestRun_OnboardThenStatus_EndToEnd(t *testing.T) {
	a, _, _, _ := newApp(t)
	var stderr bytes.Buffer
	ctx := context.Background()

	if code := run(ctx, a, []string{"onboard", "owner/repo"}, &stderr); code != 0 {
		t.Fatalf("onboard exit = %d, stderr=%s", code, stderr.String())
	}
	scn := writeScenario(t, richScenario("A-1", "x", "T1"))
	if code := run(ctx, a, []string{"intake", "--project", "repo", "--file", scn}, &stderr); code != 0 {
		t.Fatalf("intake exit = %d, stderr=%s", code, stderr.String())
	}
	if code := run(ctx, a, []string{"status", "--project", "repo", "--json"}, &stderr); code != 0 {
		t.Fatalf("status exit = %d, stderr=%s", code, stderr.String())
	}
	if code := run(ctx, a, []string{"pause", "--project", "repo"}, &stderr); code != 0 {
		t.Fatalf("pause exit = %d, stderr=%s", code, stderr.String())
	}
}

func TestRun_MissingRequiredFlag_NonZero(t *testing.T) {
	a, _, _, _ := newApp(t)
	var stderr bytes.Buffer
	if code := run(context.Background(), a, []string{"status"}, &stderr); code == 0 {
		t.Fatalf("status with no --project must exit non-zero")
	}
}

// TestConductorctl_Intake_RealFixture_RichSchema proves the delegated path accepts
// a realistic multi-field rich scenario (quoted title with a colon, flow-list deps,
// acceptance, repo-external holdout) — replacing the old hand-rolled-parser unit
// test now that conductorctl delegates to internal/intake.
func TestConductorctl_Intake_RealFixture_RichSchema(t *testing.T) {
	ctx := context.Background()
	a, store, _, _ := newApp(t)
	if _, err := a.onboard(ctx, "owner/repo", ""); err != nil {
		t.Fatalf("onboard: %v", err)
	}
	// Seed the deps C-1 references so they resolve.
	for _, id := range []string{"A-3", "B-1", "B-2", "B-3"} {
		if err := store.CreateTask(ctx, statestore.Task{ID: id, ProjectID: "repo", Lane: "x", Tier: "T1", Status: registry.StatusTodo}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	body := strings.Join([]string{
		"id: C-1",
		`title: "Conductor tick: pick -> ... (one fresh-context tick)"`,
		"lane: conductor",
		"tier: T2",
		`deps: ["A-3", "B-1", "B-2", "B-3"]`,
		"acceptance:",
		`  - "one fresh-context tick advances exactly one task"`,
		`hidden_holdout_ref: "store://holdouts/C-1/spec.yaml"`,
		"",
	}, "\n")
	path := writeScenario(t, body)
	if _, err := a.intake(ctx, "repo", path); err != nil {
		t.Fatalf("intake real fixture: %v", err)
	}
	scn, err := store.GetScenario(ctx, "C-1")
	if err != nil {
		t.Fatalf("scenario not persisted: %v", err)
	}
	if scn.Lane != "conductor" || scn.Tier != "T2" || len(scn.Deps) != 4 {
		t.Fatalf("bad scenario: %+v", scn)
	}
	if !strings.Contains(scn.Title, "Conductor tick") {
		t.Fatalf("title with colon mis-parsed: %q", scn.Title)
	}
}
