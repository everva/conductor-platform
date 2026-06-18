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
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/everva/conductor-platform/internal/conductor"
	"github.com/everva/conductor-platform/internal/intake"
	"github.com/everva/conductor-platform/internal/statestore"
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
	baseBranch := req.BaseBranch
	if baseBranch == "" {
		baseBranch = controlDefaultBaseBranch
	}

	ctx := r.Context()

	// Idempotency by repo (mirrors conductorctl): an existing project for the same
	// repo is returned unchanged with 200.
	existing, err := s.store.ListProjects(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
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
		writeError(w, http.StatusInternalServerError, "internal error")
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
		writeError(w, http.StatusInternalServerError, "internal error")
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
			writeError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project": id, "approved_task": approvedID})
}

// --- control DTO + helpers ---

// intakeResultDTO is the JSON shape returned by POST intake: the created and
// skipped scenario IDs, always as arrays (never null) so a no-op intake encodes
// "[]". Mirrors intake.IntakeResult.
type intakeResultDTO struct {
	Created []string `json:"created"`
	Skipped []string `json:"skipped"`
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
	writeError(w, http.StatusInternalServerError, "internal error")
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
