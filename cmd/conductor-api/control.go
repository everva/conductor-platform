// Control surface for the conductor-api gateway (ADR-0025, task 3A-3): the six
// POST endpoints that MUTATE the shared platform intent — onboard, intake,
// pause, resume, abort, approve. They are "conductorctl over HTTP": each handler
// REUSES the exact same conductor/intake/statestore seams the CLI does, so the
// gateway and the CLI stay behaviorally identical and there is NO second source
// of truth and NO new business logic here.
//
// Per ADR-0025's "store-reflection, NO direct daemon command": these handlers
// only write durable state to the SHARED store (Project.Paused,
// Task.AbortRequested, Task.Approved, new Project/Scenario/Task rows). The
// daemon honors that state on its next tick — the gateway never signals the
// daemon directly. Every handler is bearer-authenticated (wired in routes()),
// emits secret-free application/json, and maps the seams' sentinel errors to the
// right HTTP status.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/everva/conductor-platform/internal/conductor"
	"github.com/everva/conductor-platform/internal/gitsafe"
	"github.com/everva/conductor-platform/internal/intake"
	"github.com/everva/conductor-platform/internal/statestore"
	"gopkg.in/yaml.v3"
)

// controlDefaultBaseBranch is the integration branch onboarded projects default
// to when the request omits base_branch. It REPLICATES conductorctl's
// defaultBaseBranch by VALUE (the cmd/conductorctl package is a separate main and
// must not be imported); both must stay "develop" so onboarding via the gateway
// and via the CLI produce identical projects.
const controlDefaultBaseBranch = "develop"

// maxIntakeBody bounds the scenario-YAML request body for POST intake so a
// pathological upload cannot exhaust memory. Scenario batches are small text; a
// generous cap is plenty.
const maxIntakeBody = 1 << 20 // 1 MiB

// onboardRequest is the JSON body of POST /projects. base_branch is optional and
// defaults to controlDefaultBaseBranch when blank.
type onboardRequest struct {
	Repo       string `json:"repo"`
	BaseBranch string `json:"base_branch"`
}

// approveRequest is the OPTIONAL JSON body of POST /projects/{id}/approve. An
// empty/absent task_id auto-resolves the project's unique awaiting-approval task,
// exactly as conductorctl does.
type approveRequest struct {
	TaskID string `json:"task_id"`
}

// distillRequest is the JSON body of POST /projects/{id}/distill: the free-text
// conversation to distill into PROPOSED scenarios. An empty/absent conversation
// is rejected (the distiller has nothing to work on).
type distillRequest struct {
	Conversation string `json:"conversation"`
}

// handleOnboard: POST /projects — register a project for repo, idempotent by
// repo. Mirrors conductorctl's onboard: derive a stable ID from the repo, default
// the base branch, and return an existing project unchanged rather than
// duplicating it. 201 on create, 200 on an existing match; empty repo → 400.
func (s *apiServer) handleOnboard(w http.ResponseWriter, r *http.Request) {
	var req onboardRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if req.Repo == "" {
		writeError(w, http.StatusBadRequest, "repo is required")
		return
	}
	// Reject a repo that could be mis-parsed as a git CLI option: the daemon's
	// provisioner runs `git clone -- <repo>` with repo as a positional, so a value
	// starting with "-" (e.g. "--upload-pack=<cmd>") must never reach it. gitsafe also
	// rejects embedded whitespace/control chars. Defense-in-depth (the provisioner's
	// "--" is the exec-site guard); still allows owner/name, https URLs, abs paths.
	if !gitsafe.ValidArg(req.Repo) {
		writeError(w, http.StatusBadRequest, "invalid repo")
		return
	}
	// An explicit base branch also flows to `git fetch`/`git worktree` as a
	// positional, so it gets the same guard; an empty one defaults below, safely.
	if req.BaseBranch != "" && !gitsafe.ValidArg(req.BaseBranch) {
		writeError(w, http.StatusBadRequest, "invalid base_branch")
		return
	}
	baseBranch := req.BaseBranch
	if baseBranch == "" {
		baseBranch = controlDefaultBaseBranch
	}

	ctx := r.Context()

	// Idempotency by repo (mirrors conductorctl): an existing project for the same
	// repo is returned unchanged with 200.
	existing, err := s.store.ListProjects(ctx)
	if err != nil {
		s.serverError(w, "onboard: list projects", err)
		return
	}
	for _, p := range existing {
		if p.Repo == req.Repo {
			writeJSON(w, http.StatusOK, toProjectDTO(p))
			return
		}
	}

	p := statestore.Project{
		ID:         projectIDForRepoControl(req.Repo),
		Repo:       req.Repo,
		BaseBranch: baseBranch,
		Readiness:  "ready",
	}
	if err := s.store.CreateProject(ctx, p); err != nil {
		s.serverError(w, "onboard: create project", err)
		return
	}
	writeJSON(w, http.StatusCreated, toProjectDTO(p))
}

// handleIntake: POST /projects/{id}/intake — the request body is the scenario
// YAML (raw, possibly multi-document). It REUSES the intake pipeline
// (LoadYAML → Intake) over the shared store, never a temp file. Validation/parse
// errors surface as a 400 with the pipeline's descriptive (secret-free) message;
// success returns 200 with the IntakeResult.
func (s *apiServer) handleIntake(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	body, err := io.ReadAll(io.LimitReader(r.Body, maxIntakeBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read body")
		return
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "scenario yaml body is required")
		return
	}

	scenarios, err := intake.LoadYAML(body)
	if err != nil {
		// Parse error: descriptive, never secret.
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	res, err := intake.Intake(r.Context(), s.store, id, scenarios)
	if err != nil {
		// A missing project surfaces as a wrapped ErrNotFound from the pipeline.
		if errors.Is(err, statestore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		// Validation errors (shape, dangling deps, …) are descriptive and
		// secret-free; surface the message as a 400.
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, intakeResultDTO{
		Created: nonNil(res.Created),
		Skipped: nonNil(res.Skipped),
	})
}

// handleDistill: POST /projects/{id}/distill — turn the request body's free-text
// conversation into PROPOSED, shape-validated scenarios for human review. This is
// an ADDITIVE drafting helper (ADR-0005, ADR-0012 §4): it REUSES the existing
// intake.Distiller seam and PERSISTS NOTHING. The response carries both the
// structured scenarios (for the UI to render) and an intake-ready YAML string that
// the EXISTING POST /projects/{id}/intake accepts verbatim, so the human approve
// step is just re-POSTing that YAML.
//
// When the wired distiller implements the clarifying seam (ADR-0047) it may instead
// return SPECIFIC clarifying questions (CC AskUserQuestion) when the conversation is
// ambiguous, rather than guessing — the additive `questions` field. Honest,
// never-fake-green mapping of the distiller's outcome:
//   - scenarios distilled           → 200 {"scenarios":[...],"yaml":"..."}
//   - clarification needed           → 200 {"scenarios":[],"questions":[...]}
//   - intake.ErrNoScenarios          → 422 (the model gave nothing usable)
//   - intake.ErrMalformed{Scenarios,Questions} → 422 (a block was found but invalid)
//   - any other (runner/exec) error  → 502 (never leaking command/secret details)
//   - nil distiller (misconfigured)  → 501
//
// The conversation is NEVER logged or echoed (it may carry sensitive context).
func (s *apiServer) handleDistill(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if s.distiller == nil {
		writeError(w, http.StatusNotImplemented, "distiller not configured")
		return
	}

	var req distillRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if strings.TrimSpace(req.Conversation) == "" {
		writeError(w, http.StatusBadRequest, "conversation is required")
		return
	}

	ctx := r.Context()

	// Distill against a REAL project so the UI never drafts into the void; this is
	// a read-only existence check (no mutation).
	if _, err := s.store.GetProject(ctx, id); err != nil {
		if errors.Is(err, statestore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		s.serverError(w, "distill: get project", err)
		return
	}

	// Prefer the clarifying seam when the wired distiller supports it (ADR-0047):
	// the model may answer with SPECIFIC clarifying questions instead of guessing.
	// A distiller that only knows the frozen Distill path falls back to it, so the
	// scenarios-only behavior is byte-identical for legacy/fake distillers.
	var outcome intake.DistillOutcome
	var err error
	if cd, ok := s.distiller.(intake.ClarifyingDistiller); ok {
		outcome, err = cd.DistillOrClarify(ctx, req.Conversation)
	} else {
		var scenarios []intake.Scenario
		scenarios, err = s.distiller.Distill(ctx, req.Conversation)
		outcome.Scenarios = scenarios
	}
	if err != nil {
		status, msg := distillErrorResponse(err)
		writeError(w, status, msg)
		return
	}

	// Guarantee the approved scenario actually lands: rename any ID that collides with
	// existing project work so intake never silently skips it (no-op for a clarifying
	// turn, which carries no scenarios).
	outcome.Scenarios = s.uniquifyScenarioIDs(ctx, id, outcome.Scenarios)

	// A clarifying turn returns questions (ADDITIVE field, empty scenarios); a
	// scenarios proposal returns {scenarios, yaml} UNCHANGED (questions omitempty).
	dto, err := distillResultDTOFromOutcome(outcome)
	if err != nil {
		s.serverError(w, "distill: marshal yaml", err)
		return
	}
	writeJSON(w, http.StatusOK, dto)
}

// distillErrorResponse maps a distiller error to an HTTP status + secret-free message
// (never the conversation or a command line). Shared by handleDistill and the
// streaming variant so the honest never-fake-green mapping is identical:
//   - intake.ErrNoScenarios                     → 422
//   - intake.ErrMalformed{Scenarios,Questions}  → 422 (validation/decode detail)
//   - any other (runner/exec) error             → 502 (opaque "distiller failed")
func distillErrorResponse(err error) (int, string) {
	switch {
	case errors.Is(err, intake.ErrNoScenarios):
		return http.StatusUnprocessableEntity, "no scenarios could be distilled from the conversation"
	case errors.Is(err, intake.ErrMalformedScenarios), errors.Is(err, intake.ErrMalformedQuestions):
		return http.StatusUnprocessableEntity, err.Error()
	default:
		return http.StatusBadGateway, "distiller failed"
	}
}

// uniquifyScenarioIDs rewrites distilled scenario IDs so each is unique against the
// project's EXISTING scenario/task IDs and within the batch. The distiller mints
// generic IDs (e.g. "A-1") that collide with earlier work; intake then SILENTLY skips a
// colliding scenario, so the director's approved job never reaches the board. Renaming a
// collision to "<id>-2", "<id>-3", ... guarantees every approved scenario lands. The
// derived "pg://holdouts/<id>" holdout ref is kept in sync; an explicit non-derived ref
// is untouched, and a blank ID is left to upstream validation. (The director distiller
// is single-scenario, so intra-batch deps are not remapped.)
func (s *apiServer) uniquifyScenarioIDs(ctx context.Context, projectID string, scenarios []intake.Scenario) []intake.Scenario {
	if len(scenarios) == 0 {
		return scenarios
	}
	taken := make(map[string]struct{})
	if tasks, err := s.store.ListTasks(ctx, projectID); err == nil {
		for _, t := range tasks {
			taken[t.ID] = struct{}{}
		}
	}
	if scs, err := s.store.ListScenarios(ctx, projectID); err == nil {
		for _, sc := range scs {
			taken[sc.ID] = struct{}{}
		}
	}
	for i := range scenarios {
		oldID := strings.TrimSpace(scenarios[i].ID)
		if oldID == "" {
			continue
		}
		newID := oldID
		if _, dup := taken[newID]; dup {
			for n := 2; ; n++ {
				cand := fmt.Sprintf("%s-%d", oldID, n)
				if _, t := taken[cand]; !t {
					newID = cand
					break
				}
			}
		}
		taken[newID] = struct{}{}
		if newID != oldID {
			scenarios[i].ID = newID
			if scenarios[i].HoldoutRef == "pg://holdouts/"+oldID {
				scenarios[i].HoldoutRef = "pg://holdouts/" + newID
			}
		}
	}
	return scenarios
}

// distillResultDTOFromOutcome builds the wire DTO for a successful distill outcome: a
// clarifying turn (questions + empty scenarios) or a scenarios proposal (scenarios +
// intake-ready yaml). Shared by handleDistill and handleDistillStream.
func distillResultDTOFromOutcome(outcome intake.DistillOutcome) (distillResultDTO, error) {
	if len(outcome.Questions) > 0 {
		qd := make([]questionDTO, 0, len(outcome.Questions))
		for _, q := range outcome.Questions {
			qd = append(qd, toQuestionDTO(q))
		}
		return distillResultDTO{Scenarios: []scenarioDTO{}, Questions: qd}, nil
	}
	yamlStr, err := marshalIntakeYAML(outcome.Scenarios)
	if err != nil {
		return distillResultDTO{}, err
	}
	dtos := make([]scenarioDTO, 0, len(outcome.Scenarios))
	for _, sc := range outcome.Scenarios {
		dtos = append(dtos, toScenarioDTO(sc))
	}
	// Faz-S S4: surface the auto-generated holdout (if any) for the director to review.
	return distillResultDTO{Scenarios: dtos, YAML: yamlStr, Holdout: outcome.Holdout}, nil
}

// handleDistillStream: POST /projects/{id}/distill/stream — the STREAMING variant of
// handleDistill (Q3c.4). It runs the same distill but emits Server-Sent Events so the
// UI can show live progress during a long model call:
//
//   - event: progress  data: {"lines":N}   — N lines of model output produced so far
//   - event: result    data: <distillResultDTO>  — the final scenarios OR questions
//   - event: error     data: {"status":S,"error":M}  — an honest 422/502 outcome
//
// SECURITY: only the line COUNT is streamed, NEVER the line content — the model's
// prose may echo the (possibly sensitive) conversation, which is never logged or
// echoed. Pre-stream failures (bad body, unknown project, no distiller) are normal
// HTTP errors; once the SSE stream opens (200) the distill outcome is in-band.
func (s *apiServer) handleDistillStream(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if s.distiller == nil {
		writeError(w, http.StatusNotImplemented, "distiller not configured")
		return
	}
	var req distillRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if strings.TrimSpace(req.Conversation) == "" {
		writeError(w, http.StatusBadRequest, "conversation is required")
		return
	}

	ctx := r.Context()
	if _, err := s.store.GetProject(ctx, id); err != nil {
		if errors.Is(err, statestore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		s.serverError(w, "distill stream: get project", err)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		s.serverError(w, "distill stream", errors.New("streaming unsupported by the response writer"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering of the stream
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// sendEvent writes one SSE frame and flushes. It returns false once a write fails
	// (client gone) so the caller stops trying. onLine runs on THIS goroutine (the
	// runner scans synchronously), so no locking is needed.
	clientGone := false
	sendEvent := func(event string, payload any) {
		if clientGone {
			return
		}
		b, mErr := json.Marshal(payload)
		if mErr != nil {
			return
		}
		if _, wErr := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); wErr != nil {
			clientGone = true
			return
		}
		flusher.Flush()
	}

	lines := 0
	onLine := func(string) {
		lines++
		sendEvent("progress", map[string]int{"lines": lines}) // COUNT only — never content
	}

	var outcome intake.DistillOutcome
	var derr error
	switch d := s.distiller.(type) {
	case intake.StreamingDistiller:
		outcome, derr = d.DistillOrClarifyStream(ctx, req.Conversation, onLine)
	case intake.ClarifyingDistiller:
		outcome, derr = d.DistillOrClarify(ctx, req.Conversation)
	default:
		var sc []intake.Scenario
		sc, derr = s.distiller.Distill(ctx, req.Conversation)
		outcome.Scenarios = sc
	}

	if derr != nil {
		status, msg := distillErrorResponse(derr)
		sendEvent("error", map[string]any{"status": status, "error": msg})
		return
	}
	outcome.Scenarios = s.uniquifyScenarioIDs(ctx, id, outcome.Scenarios)
	dto, err := distillResultDTOFromOutcome(outcome)
	if err != nil {
		sendEvent("error", map[string]any{"status": http.StatusInternalServerError, "error": "could not render result"})
		return
	}
	sendEvent("result", dto)
}

// handlePause: POST /projects/{id}/pause — set the project's first-class paused
// run-state via the SAME StorePauser the daemon and CLI use. Idempotent. An
// unknown project surfaces as ErrNotFound → 404.
func (s *apiServer) handlePause(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := conductor.NewStorePauser(s.store).Pause(r.Context(), id); err != nil {
		s.writeSeamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": id, "paused": true})
}

// handleResume: POST /projects/{id}/resume — clear the project's paused
// run-state. Idempotent. Unknown project → 404.
func (s *apiServer) handleResume(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := conductor.NewStorePauser(s.store).Resume(r.Context(), id); err != nil {
		s.writeSeamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": id, "paused": false})
}

// handleAbort: POST /projects/{id}/abort — request cancellation of the project's
// currently-running task via the SAME StoreAborter. Nothing running → 409
// (well-formed request, nothing to act on). Unknown project also surfaces as
// ErrNothingRunning (no lease) → 409, mirroring conductorctl.
func (s *apiServer) handleAbort(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	taskID, err := conductor.NewStoreAborter(s.store).RequestAbort(r.Context(), id)
	if err != nil {
		if errors.Is(err, conductor.ErrNothingRunning) {
			writeError(w, http.StatusConflict, "no task running to abort")
			return
		}
		s.serverError(w, "abort", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": id, "aborted_task": taskID})
}

// handleApprove: POST /projects/{id}/approve — approve a held awaiting-approval
// task via the SAME StoreApprover. The body is OPTIONAL JSON {"task_id":"..."};
// an empty/absent task_id auto-resolves the unique awaiting-approval task. A
// "nothing to approve" / "ambiguous" outcome → 409 with the seam's message; an
// unknown project/task (ErrNotFound) → 404.
func (s *apiServer) handleApprove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req approveRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	// Existence-check the project first (review F2): the auto-resolve path lists
	// tasks, and an unknown project lists EMPTY with no error, which would
	// misreport as 409 "no task awaiting approval" (implying the project exists).
	// A missing project must be 404, matching the handler contract.
	if _, err := s.store.GetProject(r.Context(), id); err != nil {
		if errors.Is(err, statestore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		s.serverError(w, "approve: get project", err)
		return
	}

	approvedID, err := conductor.NewStoreApprover(s.store).RequestApprove(r.Context(), id, req.TaskID)
	if err != nil {
		switch {
		case errors.Is(err, conductor.ErrNothingToApprove):
			writeError(w, http.StatusConflict, "no task awaiting approval")
		case errors.Is(err, conductor.ErrAmbiguousApproval):
			writeError(w, http.StatusConflict, "multiple tasks awaiting approval; specify task_id")
		case errors.Is(err, statestore.ErrNotFound):
			writeError(w, http.StatusNotFound, "project not found")
		default:
			s.serverError(w, "approve", err)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": id, "approved_task": approvedID})
}

// handleRetry: POST /projects/{id}/tasks/{task}/retry — reset a BLOCKED task back to
// "ready" so the agent re-picks it (PickReady only takes todo/ready). A blocked task is
// otherwise stranded: it failed develop/verify and there was no way to re-run it short of
// re-intaking the scenario. The registry transition blocked→ready is valid (registry.go
// transitions table). Only a blocked task can be retried (else 409); the stale AbortRequested
// flag is cleared so the re-run is not immediately aborted. Store-reflection only (ADR-0025):
// the agent honors the ready status on its next lease. Idempotency-friendly + bearer-authed.
func (s *apiServer) handleRetry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	taskID := r.PathValue("task")
	ctx := r.Context()

	if _, err := s.store.GetProject(ctx, id); err != nil {
		if errors.Is(err, statestore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		s.serverError(w, "retry: get project", err)
		return
	}

	task, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		if errors.Is(err, statestore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
		s.serverError(w, "retry: get task", err)
		return
	}
	if task.ProjectID != id {
		writeError(w, http.StatusNotFound, "task not found in project")
		return
	}
	if task.Status != "blocked" {
		writeError(w, http.StatusConflict, "only a blocked task can be retried")
		return
	}

	if err := s.updateTask(ctx, taskID, func(t *statestore.Task) {
		t.Status = "ready"       // blocked→ready (valid registry transition); re-pickable
		t.AbortRequested = false // clear any stale abort so the re-run is not killed on start
		t.LastError = ""         // clear the prior block reason — the board shows it retrying clean
	}); err != nil {
		s.serverError(w, "retry: update task", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": id, "task": taskID, "status": "ready"})
}

// --- control DTO + helpers ---

// intakeResultDTO is the JSON shape returned by POST intake: the created and
// skipped scenario IDs, always as arrays (never null) so a no-op intake encodes
// "[]". Mirrors intake.IntakeResult.
type intakeResultDTO struct {
	Created []string `json:"created"`
	Skipped []string `json:"skipped"`
}

// scenarioDTO is the snake_case JSON shape of a PROPOSED distilled scenario. We do
// NOT serialize intake.Scenario directly: that type carries only yaml tags, so the
// JSON encoder would emit Go PascalCase field names (ID, Title, …) — inconsistent
// with every other gateway DTO (project/task/host/lease are snake_case) and awkward
// for the web client (review sweep S-1). This DTO pins the snake_case contract.
type scenarioDTO struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Lane       string   `json:"lane"`
	Tier       string   `json:"tier"`
	Deps       []string `json:"deps"`
	Acceptance []string `json:"acceptance"`
	HoldoutRef string   `json:"hidden_holdout_ref"`
}

// toScenarioDTO maps a distilled intake.Scenario onto the snake_case wire shape,
// non-nilling the slices so they encode as [] not null. The rich-only fields
// (PublicTestRef/Outline/Notes) are intentionally omitted — they are not part of
// the proposed-scenario review contract (mirroring ToStateScenario's drop).
func toScenarioDTO(s intake.Scenario) scenarioDTO {
	deps := s.Deps
	if deps == nil {
		deps = []string{}
	}
	acc := s.Acceptance
	if acc == nil {
		acc = []string{}
	}
	return scenarioDTO{
		ID: s.ID, Title: s.Title, Lane: s.Lane, Tier: s.Tier,
		Deps: deps, Acceptance: acc, HoldoutRef: s.HoldoutRef,
	}
}

// distillResultDTO is the JSON shape returned by POST distill: the structured
// PROPOSED scenarios (snake_case scenarioDTO) and the intake-ready YAML string. The
// yaml field is exactly what POST /intake accepts, so the human approve step is a
// verbatim re-POST of it. Nothing is persisted.
//
// Questions is the ADDITIVE clarifying-turn field (ADR-0047): when the distiller
// asks for more detail instead of distilling, it carries the structured
// AskUserQuestion-style questions and scenarios/yaml are empty. It is omitempty so a
// scenarios response is byte-identical to the pre-ADR-0047 body for legacy clients.
type distillResultDTO struct {
	Scenarios []scenarioDTO `json:"scenarios"`
	YAML      string        `json:"yaml"`
	Questions []questionDTO `json:"questions,omitempty"`
	// Holdout is the OPTIONAL auto-generated hidden-holdout test (Faz-S S4): path -> file content,
	// for the director to REVIEW before approving. Omitted when the model emitted none. On approve
	// the editor PUTs it to /holdouts/{id} (S5) so the gate can run it.
	Holdout map[string]string `json:"holdout,omitempty"`
}

// questionDTO is the snake_case JSON shape of a clarifying question (the mirror of
// CC's AskUserQuestion, ADR-0047). As with scenarioDTO we do NOT serialize
// intake.Question directly (it carries only yaml tags) — this pins the snake_case
// wire contract the web client consumes.
type questionDTO struct {
	Question    string              `json:"question"`
	Header      string              `json:"header"`
	Options     []questionOptionDTO `json:"options"`
	MultiSelect bool                `json:"multi_select"`
}

// questionOptionDTO is one offered choice of a questionDTO (label + description).
type questionOptionDTO struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// toQuestionDTO maps an intake.Question onto the snake_case wire shape, non-nilling
// the options slice so it encodes as [] not null.
func toQuestionDTO(q intake.Question) questionDTO {
	opts := make([]questionOptionDTO, 0, len(q.Options))
	for _, o := range q.Options {
		opts = append(opts, questionOptionDTO{Label: o.Label, Description: o.Description})
	}
	return questionDTO{
		Question:    q.Question,
		Header:      q.Header,
		Options:     opts,
		MultiSelect: q.MultiSelect,
	}
}

// marshalIntakeYAML renders the distilled scenarios into the EXACT multi-document
// YAML stream that intake.LoadYAML accepts: each scenario is one YAML document,
// separated by "---", encoded via the Scenario struct's existing yaml tags (so the
// round-trip through LoadYAML reproduces the same scenarios). LoadYAML decodes ONE
// Scenario per document — it is NOT a `scenarios:` wrapper — so we emit a plain
// document stream, never the distiller's internal fenced/wrapped form.
func marshalIntakeYAML(scenarios []intake.Scenario) (string, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	for i := range scenarios {
		if err := enc.Encode(scenarios[i]); err != nil {
			_ = enc.Close()
			return "", err
		}
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// toProjectDTO maps a statestore.Project onto the SAME projectDTO shape the read
// surface (GET /projects) returns, so onboard's response is field-identical to a
// later list/read of the project.
func toProjectDTO(p statestore.Project) projectDTO {
	return projectDTO{
		ID:               p.ID,
		Repo:             p.Repo,
		BaseBranch:       p.BaseBranch,
		HostID:           p.HostID,
		Readiness:        p.Readiness,
		RecipePointer:    p.RecipePointer,
		GovernancePolicy: p.GovernancePolicy,
		Paused:           p.Paused,
	}
}

// writeSeamError maps a pause/resume seam error to an HTTP status: a wrapped
// statestore.ErrNotFound (unknown project) → 404; anything else → 500. The seam
// errors are wrapped but secret-free; the response body is still a fixed string.
func (s *apiServer) writeSeamError(w http.ResponseWriter, err error) {
	if errors.Is(err, statestore.ErrNotFound) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	s.serverError(w, "seam", err)
}

// decodeJSONBody decodes an OPTIONAL JSON request body into v. An empty body is
// NOT an error (v keeps its zero value), so endpoints with optional bodies
// (onboard, approve) accept "no body" cleanly; a non-empty body that is invalid
// JSON is an error. It does not consult Content-Type.
func decodeJSONBody(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxIntakeBody))
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, v)
}

// projectIDForRepoControl derives a stable project ID from a repo string so
// re-onboard is idempotent. It REPLICATES conductorctl.projectIDForRepo by value
// (that package is a separate main and must not be imported) so a project
// onboarded via the gateway has the SAME ID as one onboarded via the CLI: slug
// the repo's last path segment, falling back to the raw repo.
func projectIDForRepoControl(repo string) string {
	trimmed := strings.TrimSuffix(repo, ".git")
	trimmed = strings.TrimSuffix(trimmed, "/")
	if i := strings.LastIndexAny(trimmed, "/:"); i >= 0 && i+1 < len(trimmed) {
		trimmed = trimmed[i+1:]
	}
	if trimmed == "" {
		return repo
	}
	return trimmed
}
