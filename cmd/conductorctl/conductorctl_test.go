package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

func TestConductorctl_Intake_CreatesTaskAndScenario(t *testing.T) {
	ctx := context.Background()
	a, store, _, _ := newApp(t)
	if _, err := a.onboard(ctx, "owner/repo", ""); err != nil {
		t.Fatalf("onboard: %v", err)
	}

	path := writeScenario(t, `id: A-1
title: "first task"
lane: statestore
tier: T1
deps: []
`)
	scenario, task, err := a.intake(ctx, "repo", path)
	if err != nil {
		t.Fatalf("intake: %v", err)
	}
	if scenario.ID != "A-1" || scenario.Lane != "statestore" || scenario.Tier != "T1" {
		t.Fatalf("unexpected scenario: %+v", scenario)
	}
	if task.ID != "A-1" || task.Status != registry.StatusTodo || task.ScenarioID != "A-1" {
		t.Fatalf("unexpected task: %+v", task)
	}
	// Both persisted in the shared store.
	if _, err := store.GetScenario(ctx, "A-1"); err != nil {
		t.Fatalf("scenario not persisted: %v", err)
	}
	if _, err := store.GetTask(ctx, "A-1"); err != nil {
		t.Fatalf("task not persisted: %v", err)
	}
}

func TestConductorctl_Intake_KnownDep_OK(t *testing.T) {
	ctx := context.Background()
	a, _, _, _ := newApp(t)
	if _, err := a.onboard(ctx, "owner/repo", ""); err != nil {
		t.Fatalf("onboard: %v", err)
	}
	// First task with no deps.
	p1 := writeScenario(t, "id: A-1\nlane: x\ntier: T1\ndeps: []\n")
	if _, _, err := a.intake(ctx, "repo", p1); err != nil {
		t.Fatalf("intake A-1: %v", err)
	}
	// Second task depends on the existing A-1: accepted.
	p2 := writeScenario(t, "id: A-2\nlane: x\ntier: T1\ndeps: [\"A-1\"]\n")
	_, task, err := a.intake(ctx, "repo", p2)
	if err != nil {
		t.Fatalf("intake A-2 with known dep: %v", err)
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
	path := writeScenario(t, "id: B-1\nlane: x\ntier: T2\ndeps: [\"DOES-NOT-EXIST\"]\n")
	_, _, err := a.intake(ctx, "repo", path)
	if err == nil {
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
	// Missing required `lane` and `tier`.
	path := writeScenario(t, "id: C-9\ntitle: broken\n")
	if _, _, err := a.intake(ctx, "repo", path); err == nil {
		t.Fatalf("expected error for missing fields")
	}
	if _, gerr := store.GetScenario(ctx, "C-9"); gerr == nil {
		t.Fatalf("no partial write: scenario must not exist")
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
	scn := writeScenario(t, "id: A-1\nlane: x\ntier: T1\ndeps: []\n")
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

// TestParseScenario_RealFixture parses an actual repo scenario file shape to prove
// the parser handles flow-list deps and quoted titles, not just synthetic input.
func TestParseScenario_RealFixture(t *testing.T) {
	body := `id: C-1
title: "Conductor tick: pick -> ... (one fresh-context tick)"
lane: conductor
tier: T2
deps: ["A-3", "B-1", "B-2", "B-3"]
`
	path := writeScenario(t, body)
	doc, err := parseScenarioFile(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if doc.ID != "C-1" || doc.Lane != "conductor" || doc.Tier != "T2" {
		t.Fatalf("bad scalars: %+v", doc)
	}
	if len(doc.Deps) != 4 || doc.Deps[0] != "A-3" || doc.Deps[3] != "B-3" {
		t.Fatalf("bad deps: %+v", doc.Deps)
	}
	if !strings.Contains(doc.Title, "Conductor tick") {
		t.Fatalf("title with colon mis-parsed: %q", doc.Title)
	}
}
