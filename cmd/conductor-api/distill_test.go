package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/everva/conductor-platform/internal/intake"
	"github.com/everva/conductor-platform/internal/statestore"
)

// stubDistiller is a fake intake.Distiller that returns crafted output without
// spawning any real `claude` — keeping the gate offline and deterministic.
type stubDistiller struct {
	scenarios []intake.Scenario
	err       error
}

func (d stubDistiller) Distill(_ context.Context, _ string) ([]intake.Scenario, error) {
	return d.scenarios, d.err
}

// distillServer builds an apiServer over a fresh in-memory store with the given
// distiller injected (the test seam — no real claude), the fixed token + clock.
func distillServer(d intake.Distiller) (*apiServer, statestore.StateStore) {
	store := statestore.NewMemoryStore()
	return &apiServer{store: store, token: testToken, clock: fixedClock, distiller: d}, store
}

// sampleScenarios is a valid two-scenario batch (A-2 deps A-1) that passes
// Scenario.Validate and exercises intra-batch dep resolution at intake time.
func sampleScenarios() []intake.Scenario {
	return []intake.Scenario{
		{
			ID:         "A-1",
			Title:      "First distilled task",
			Lane:       "backend",
			Tier:       "T1",
			Deps:       []string{},
			Acceptance: []string{"does the first thing"},
			HoldoutRef: "store://holdouts/A-1/holdout_test.go",
		},
		{
			ID:         "A-2",
			Title:      "Second distilled task depends on first",
			Lane:       "backend",
			Tier:       "T2",
			Deps:       []string{"A-1"},
			Acceptance: []string{"does the second thing"},
			HoldoutRef: "store://holdouts/A-2/holdout_test.go",
		},
	}
}

// TestDistillHappyPathAndIntakeRoundTrip is the key correctness proof: a
// conversation distills to 200 with the expected scenarios, and the returned YAML
// round-trips — POSTing it verbatim to the EXISTING /intake handler ingests the
// SAME scenarios/tasks.
func TestDistillHappyPathAndIntakeRoundTrip(t *testing.T) {
	want := sampleScenarios()
	s, store := distillServer(stubDistiller{scenarios: want})
	ctx := context.Background()
	mustCreate(t, store.CreateProject(ctx, statestore.Project{ID: "proj-x", Repo: "owner/x", BaseBranch: "develop"}))

	rec := doBody(t, s, http.MethodPost, "/projects/proj-x/distill", bearer(),
		`{"conversation":"build a backend that does two things"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// Lock the snake_case wire contract (review sweep S-1): scenario JSON keys must
	// be snake_case (like every other gateway DTO), NOT Go PascalCase.
	if body := rec.Body.String(); !strings.Contains(body, `"hidden_holdout_ref"`) || strings.Contains(body, `"HoldoutRef"`) {
		t.Fatalf("distill scenarios not snake_case: %s", body)
	}

	var res distillResultDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode distill result: %v", err)
	}
	if len(res.Scenarios) != 2 || res.Scenarios[0].ID != "A-1" || res.Scenarios[1].ID != "A-2" {
		t.Fatalf("scenarios = %+v, want A-1,A-2", res.Scenarios)
	}
	if res.Scenarios[1].Deps[0] != "A-1" {
		t.Fatalf("A-2 deps = %v, want [A-1]", res.Scenarios[1].Deps)
	}
	if res.YAML == "" {
		t.Fatal("yaml is empty")
	}

	// Proof 1: the yaml round-trips through intake.LoadYAML to the same scenarios.
	loaded, err := intake.LoadYAML([]byte(res.YAML))
	if err != nil {
		t.Fatalf("LoadYAML(distilled yaml): %v\nyaml:\n%s", err, res.YAML)
	}
	if len(loaded) != len(want) {
		t.Fatalf("LoadYAML produced %d scenarios, want %d", len(loaded), len(want))
	}
	for i := range want {
		if loaded[i].ID != want[i].ID || loaded[i].Title != want[i].Title ||
			loaded[i].Lane != want[i].Lane || loaded[i].Tier != want[i].Tier ||
			loaded[i].HoldoutRef != want[i].HoldoutRef {
			t.Fatalf("scenario %d round-trip mismatch:\n got=%+v\nwant=%+v", i, loaded[i], want[i])
		}
		if len(loaded[i].Acceptance) != 1 || loaded[i].Acceptance[0] != want[i].Acceptance[0] {
			t.Fatalf("scenario %d acceptance mismatch: got=%v want=%v", i, loaded[i].Acceptance, want[i].Acceptance)
		}
	}
	if len(loaded[1].Deps) != 1 || loaded[1].Deps[0] != "A-1" {
		t.Fatalf("A-2 deps round-trip = %v, want [A-1]", loaded[1].Deps)
	}

	// Proof 2: feeding that yaml VERBATIM to the EXISTING /intake handler ingests
	// the same scenarios as tasks (the real approve step).
	intakeRec := doBody(t, s, http.MethodPost, "/projects/proj-x/intake", bearer(), res.YAML)
	if intakeRec.Code != http.StatusOK {
		t.Fatalf("intake of distilled yaml status = %d, want 200; body=%s", intakeRec.Code, intakeRec.Body.String())
	}
	var ir intakeResultDTO
	if err := json.Unmarshal(intakeRec.Body.Bytes(), &ir); err != nil {
		t.Fatalf("decode intake result: %v", err)
	}
	if len(ir.Created) != 2 || ir.Created[0] != "A-1" || ir.Created[1] != "A-2" {
		t.Fatalf("intake created = %v, want [A-1 A-2]", ir.Created)
	}

	// And the tasks are actually persisted.
	g := do(t, s, http.MethodGet, "/projects/proj-x/tasks", bearer())
	var tasks []taskDTO
	if err := json.Unmarshal(g.Body.Bytes(), &tasks); err != nil {
		t.Fatalf("decode tasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Fatalf("persisted tasks = %d, want 2", len(tasks))
	}
}

// TestDistillRoundTripViaRunnerStub proves the same round-trip through the REAL
// CommandDistiller wired to a STUB runner emitting the fenced YAML the real prompt
// asks for (so the deterministic ParseScenarios contract is exercised too — still
// no real claude).
func TestDistillRoundTripViaRunnerStub(t *testing.T) {
	const stdout = `Here is my proposed plan.

<<<SCENARIOS
scenarios:
  - id: B-1
    title: "Runner-stub distilled scenario"
    lane: web
    tier: T2
    deps: []
    acceptance:
      - "renders the page"
    hidden_holdout_ref: "store://holdouts/B-1/holdout_test.go"
SCENARIOS>>>
`
	runner := func(_ context.Context, _ string) ([]byte, error) { return []byte(stdout), nil }
	s, store := distillServer(intake.NewCommandDistillerWithRunner(runner))
	ctx := context.Background()
	mustCreate(t, store.CreateProject(ctx, statestore.Project{ID: "proj-y", Repo: "owner/y", BaseBranch: "develop"}))

	rec := doBody(t, s, http.MethodPost, "/projects/proj-y/distill", bearer(),
		`{"conversation":"need a web page"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var res distillResultDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res.Scenarios) != 1 || res.Scenarios[0].ID != "B-1" {
		t.Fatalf("scenarios = %+v, want single B-1", res.Scenarios)
	}

	intakeRec := doBody(t, s, http.MethodPost, "/projects/proj-y/intake", bearer(), res.YAML)
	if intakeRec.Code != http.StatusOK {
		t.Fatalf("intake status = %d, want 200; body=%s", intakeRec.Code, intakeRec.Body.String())
	}
}

func TestDistillEmptyConversation400(t *testing.T) {
	s, store := distillServer(stubDistiller{scenarios: sampleScenarios()})
	mustCreate(t, store.CreateProject(context.Background(), statestore.Project{ID: "proj-x", Repo: "owner/x", BaseBranch: "develop"}))

	for _, body := range []string{`{"conversation":""}`, `{"conversation":"   "}`, `{}`, ``} {
		rec := doBody(t, s, http.MethodPost, "/projects/proj-x/distill", bearer(), body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %q: status = %d, want 400; body=%s", body, rec.Code, rec.Body.String())
		}
	}
}

func TestDistillInvalidJSON400(t *testing.T) {
	s, store := distillServer(stubDistiller{scenarios: sampleScenarios()})
	mustCreate(t, store.CreateProject(context.Background(), statestore.Project{ID: "proj-x", Repo: "owner/x", BaseBranch: "develop"}))

	rec := doBody(t, s, http.MethodPost, "/projects/proj-x/distill", bearer(), `{not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

func TestDistillUnknownProject404(t *testing.T) {
	s, _ := distillServer(stubDistiller{scenarios: sampleScenarios()})
	rec := doBody(t, s, http.MethodPost, "/projects/nope/distill", bearer(),
		`{"conversation":"anything"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

func TestDistillNoScenarios422(t *testing.T) {
	s, store := distillServer(stubDistiller{err: fmt.Errorf("%w: empty", intake.ErrNoScenarios)})
	mustCreate(t, store.CreateProject(context.Background(), statestore.Project{ID: "proj-x", Repo: "owner/x", BaseBranch: "develop"}))

	rec := doBody(t, s, http.MethodPost, "/projects/proj-x/distill", bearer(),
		`{"conversation":"vague chatter with no scenario"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
	if want := "no scenarios could be distilled from the conversation"; !bodyHasError(t, rec.Body.Bytes(), want) {
		t.Fatalf("body = %s, want error %q", rec.Body.String(), want)
	}
}

func TestDistillMalformed422(t *testing.T) {
	s, store := distillServer(stubDistiller{err: fmt.Errorf("%w: bad tier", intake.ErrMalformedScenarios)})
	mustCreate(t, store.CreateProject(context.Background(), statestore.Project{ID: "proj-x", Repo: "owner/x", BaseBranch: "develop"}))

	rec := doBody(t, s, http.MethodPost, "/projects/proj-x/distill", bearer(),
		`{"conversation":"produces a malformed block"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
}

func TestDistillGenericError502(t *testing.T) {
	s, store := distillServer(stubDistiller{err: errors.New("exec: claude exit 1")})
	mustCreate(t, store.CreateProject(context.Background(), statestore.Project{ID: "proj-x", Repo: "owner/x", BaseBranch: "develop"}))

	rec := doBody(t, s, http.MethodPost, "/projects/proj-x/distill", bearer(),
		`{"conversation":"trigger a runner failure"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", rec.Code, rec.Body.String())
	}
	// The opaque message must NOT leak the underlying exec detail.
	if bodyHasError(t, rec.Body.Bytes(), "exec: claude exit 1") {
		t.Fatalf("body leaked exec detail: %s", rec.Body.String())
	}
	if want := "distiller failed"; !bodyHasError(t, rec.Body.Bytes(), want) {
		t.Fatalf("body = %s, want error %q", rec.Body.String(), want)
	}
}

func TestDistillNilDistiller501(t *testing.T) {
	s, store := distillServer(nil)
	mustCreate(t, store.CreateProject(context.Background(), statestore.Project{ID: "proj-x", Repo: "owner/x", BaseBranch: "develop"}))

	rec := doBody(t, s, http.MethodPost, "/projects/proj-x/distill", bearer(),
		`{"conversation":"anything"}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501; body=%s", rec.Code, rec.Body.String())
	}
}

func TestDistillRequiresAuth(t *testing.T) {
	s, store := distillServer(stubDistiller{scenarios: sampleScenarios()})
	mustCreate(t, store.CreateProject(context.Background(), statestore.Project{ID: "proj-x", Repo: "owner/x", BaseBranch: "develop"}))

	// No token → 401.
	rec := doBody(t, s, http.MethodPost, "/projects/proj-x/distill", "", `{"conversation":"x"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no-token status = %d, want 401", rec.Code)
	}
	// Wrong token → 401.
	rec = doBody(t, s, http.MethodPost, "/projects/proj-x/distill", "Bearer wrong", `{"conversation":"x"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-token status = %d, want 401", rec.Code)
	}
}

// bodyHasError reports whether the JSON error body's "error" field contains sub.
func bodyHasError(t *testing.T, body []byte, sub string) bool {
	t.Helper()
	var m map[string]string
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode error body: %v (body=%s)", err, string(body))
	}
	return strings.Contains(m["error"], sub)
}
