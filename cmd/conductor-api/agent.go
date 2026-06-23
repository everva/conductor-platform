// Gateway-mediated host-agent API (ADR-0048, Faz G): the endpoints a performer
// host-agent calls so it can do real work WITHOUT ever touching Postgres. The host
// talks ONLY to this gateway over HTTP (bearer token, over tailscale in prod); the
// gateway owns the shared store and reuses the SAME registry/statestore seams the
// in-process daemon (cmd/conductor) uses. All DB access stays in k8s — the host-agent
// never holds the DSN, which is the whole point of the gateway-mediated model.
//
// Faz G1 (this file's first slice): the LEASE lifecycle — lease (capability-routed
// pick + acquire, with host self-registration), heartbeat (liveness), and owner-scoped
// release. The report/result/decision/merged slices are Faz G2 (additive).
package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/everva/conductor-platform/internal/events"
	"github.com/everva/conductor-platform/internal/governance"
	"github.com/everva/conductor-platform/internal/registry"
	"github.com/everva/conductor-platform/internal/statestore"
)

// agentLeaseRequest is the body of POST /projects/{id}/agent/lease: the host's stable
// id and its capability set (for capability-routing, ADR-0024/2B-2). An empty
// capabilities set means UNCONSTRAINED routing (picks regardless of Task.Requires).
type agentLeaseRequest struct {
	HostID       string   `json:"host_id"`
	Capabilities []string `json:"capabilities"`
}

// agentLeaseDTO is the snake_case wire shape of the acquired lease.
type agentLeaseDTO struct {
	ProjectID  string    `json:"project_id"`
	HostID     string    `json:"host_id"`
	TaskID     string    `json:"task_id"`
	AcquiredAt time.Time `json:"acquired_at"`
}

// agentLeaseResponse is returned on a successful lease: the leased task (what to
// develop) and the lease record. 204 (no body) means there is no pickable task right
// now (none ready, or the repo is already leased) — the agent backs off and retries.
type agentLeaseResponse struct {
	Task  taskDTO       `json:"task"`
	Lease agentLeaseDTO `json:"lease"`
}

// agentHeartbeatRequest is the body of POST /agent/heartbeat.
type agentHeartbeatRequest struct {
	HostID string `json:"host_id"`
}

// agentReleaseRequest is the body of POST /projects/{id}/agent/lease/release.
type agentReleaseRequest struct {
	HostID string `json:"host_id"`
	TaskID string `json:"task_id"`
}

// toTaskDTO maps a statestore.Task onto the snake_case wire shape, non-nilling the
// slices so they encode as [] not null. Shared by handleProjectTasks and the
// agent-API so the task contract is identical everywhere.
func toTaskDTO(t statestore.Task) taskDTO {
	return taskDTO{
		ID:             t.ID,
		ProjectID:      t.ProjectID,
		Lane:           t.Lane,
		Tier:           t.Tier,
		Status:         t.Status,
		Requires:       nonNil(t.Requires),
		Deps:           nonNil(t.Deps),
		Branch:         t.Branch,
		ScenarioID:     t.ScenarioID,
		RetryCount:     t.RetryCount,
		AbortRequested: t.AbortRequested,
		Approved:       t.Approved,
	}
}

// handleAgentLease: POST /projects/{id}/agent/lease — self-register the calling host,
// then capability-route + lease the next ready task for the project, returning it.
// This is the gateway-mediated analogue of the daemon's Tick pick+lease: it reuses
// the SAME registry.PickReady (capability gate, dep-gate, repo-per-1 lease-gate) and
// AcquireLease over the shared store, so behavior matches the in-process daemon. The
// host never touches the DB; it just gets a task to run.
//
//   - a pickable task        → 200 {task, lease}
//   - none ready / repo busy → 204 (no work; retry later)
//   - lost the acquire race  → 409 (another host leased between pick and acquire)
//   - unknown project        → 404 ; missing host_id / bad body → 400
func (s *apiServer) handleAgentLease(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req agentLeaseRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if strings.TrimSpace(req.HostID) == "" {
		writeError(w, http.StatusBadRequest, "host_id is required")
		return
	}

	ctx := r.Context()
	if _, err := s.store.GetProject(ctx, id); err != nil {
		if errors.Is(err, statestore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		s.serverError(w, "agent lease: get project", err)
		return
	}

	// Self-register the host (upsert capabilities + liveness), ADR-0024. The gateway
	// records it; the host never writes the store itself.
	if err := s.store.RegisterHost(ctx, statestore.Host{
		ID:            req.HostID,
		Capabilities:  req.Capabilities,
		LastHeartbeat: s.now(),
	}); err != nil {
		s.serverError(w, "agent lease: register host", err)
		return
	}

	// Capability-routed pick over the shared store (same logic as the daemon).
	reg := registry.NewRegistry(s.store, registry.WithCapabilities(req.Capabilities))
	task, err := reg.PickReady(ctx, id)
	if err != nil {
		if errors.Is(err, statestore.ErrNotFound) {
			// No pickable task (none ready, or repo already leased): no work right now.
			w.WriteHeader(http.StatusNoContent)
			return
		}
		s.serverError(w, "agent lease: pick ready", err)
		return
	}

	lease := statestore.Lease{ProjectID: id, HostID: req.HostID, TaskID: task.ID, AcquiredAt: s.now()}
	if err := reg.AcquireLease(ctx, lease); err != nil {
		// PickReady already lease-gates, so reaching here means another host raced us
		// and leased the repo in between (TOCTOU). Not a server fault: the agent retries.
		writeError(w, http.StatusConflict, "could not acquire lease (raced by another host)")
		return
	}

	writeJSON(w, http.StatusOK, agentLeaseResponse{
		Task:  toTaskDTO(task),
		Lease: agentLeaseDTO{ProjectID: id, HostID: req.HostID, TaskID: task.ID, AcquiredAt: lease.AcquiredAt},
	})
}

// handleAgentHeartbeat: POST /agent/heartbeat — advance the host's liveness so the
// reconcile reaper does not free its lease as a dead host. The host must have
// registered first (via a lease call); an unknown host is 404.
func (s *apiServer) handleAgentHeartbeat(w http.ResponseWriter, r *http.Request) {
	var req agentHeartbeatRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if strings.TrimSpace(req.HostID) == "" {
		writeError(w, http.StatusBadRequest, "host_id is required")
		return
	}
	if err := s.store.HostHeartbeat(r.Context(), req.HostID, s.now()); err != nil {
		if errors.Is(err, statestore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "host not registered (lease first)")
			return
		}
		s.serverError(w, "agent heartbeat", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"host_id": req.HostID, "ok": true})
}

// handleAgentReleaseLease: POST /projects/{id}/agent/lease/release — owner-scoped
// release of the project's lease (ADR-0021 C-2). Idempotent: releasing a lease the
// caller no longer owns (after a reap → re-acquire by another host) is a clean no-op,
// never an error. The agent calls this when it finishes or abandons a task.
func (s *apiServer) handleAgentReleaseLease(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	var req agentReleaseRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if strings.TrimSpace(req.HostID) == "" || strings.TrimSpace(req.TaskID) == "" {
		writeError(w, http.StatusBadRequest, "host_id and task_id are required")
		return
	}

	reg := registry.NewRegistry(s.store)
	if err := reg.ReleaseLeaseOwned(r.Context(), id, req.HostID, req.TaskID); err != nil {
		s.serverError(w, "agent release lease", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"released": true})
}

// --- Faz G2: report / result / decision / merged ------------------------------

// agentReportRequest is a live observability event the host-agent emits during
// develop/verify (progress, log, diff). The gateway publishes it on the SAME event
// bus the daemon uses, so the editor's backfill/live stream sees agent work too.
type agentReportRequest struct {
	Phase   string         `json:"phase"`
	Kind    string         `json:"kind"`
	Payload map[string]any `json:"payload"`
}

// agentCheckDTO is one gate check in a result report (name + pass/fail + evidence).
type agentCheckDTO struct {
	Name     string `json:"name"`
	Result   string `json:"result"`
	Evidence string `json:"evidence"`
}

// agentResultRequest is the host-agent's verdict after develop+verify: the gate
// result, the verified per-task branch (recorded if the task is HELD for approval),
// a summary, and the individual gate checks.
type agentResultRequest struct {
	Result  string          `json:"result"` // pass | changes-requested | blocked
	Branch  string          `json:"branch"`
	Summary string          `json:"summary"`
	Checks  []agentCheckDTO `json:"checks"`
}

// agentResultResponse tells the agent what to do next: merge (auto), hold (wait for
// the director's approval, then poll /decision), or blocked (gate failed, stop).
type agentResultResponse struct {
	Decision string `json:"decision"` // merge | hold | blocked
}

// agentDecisionResponse is the held-task decision the agent polls for.
type agentDecisionResponse struct {
	State string `json:"state"` // approved | aborted | pending
}

// agentMergedRequest reports that the agent merged the (auto or approved) task.
type agentMergedRequest struct {
	SHA string `json:"sha"`
}

// policyForProject selects the merge policy from the project's GovernancePolicy
// (ADR-0003/0048). The FAIL-SAFE default is held-for-review: an empty/unknown policy
// holds EVERY task for the director (never auto-merges by accident) — this is what
// optiway runs with. "risk-layered" is the tier-based ADR-0003 default; "auto" merges
// every tier on a green gate.
func policyForProject(p statestore.Project) *governance.Policy {
	switch strings.ToLower(strings.TrimSpace(p.GovernancePolicy)) {
	case "auto", "auto-merge":
		return governance.New(map[string]governance.MergeMode{
			governance.TierT1: governance.ModeAutoMerge, governance.TierT2: governance.ModeAutoMerge,
			governance.TierT3: governance.ModeAutoMerge, governance.TierT4: governance.ModeAutoMerge,
		})
	case "risk-layered", "tier", "tiered":
		return governance.DefaultPolicy()
	default: // "", "held", "review", "manual", unknown → hold everything (fail-safe)
		return governance.New(nil)
	}
}

// publishAgentEvent best-effort publishes an observability event on the shared bus
// (nil bus or an invalid event is a silent no-op — events never fail an agent call).
func (s *apiServer) publishAgentEvent(ctx context.Context, project, task string, phase events.Phase, kind events.Kind, payload map[string]any) {
	if s.bus == nil {
		return
	}
	ev := events.Event{Project: project, Task: task, Phase: phase, Kind: kind, Payload: payload}
	if err := ev.Validate(); err != nil {
		return
	}
	_ = s.bus.Publish(ctx, ev)
}

// updateTask re-reads the task and applies mut, persisting the result (mirroring the
// conductor's re-read-then-update so a concurrent field is not clobbered).
func (s *apiServer) updateTask(ctx context.Context, taskID string, mut func(*statestore.Task)) error {
	cur, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	mut(&cur)
	return s.store.UpdateTask(ctx, cur)
}

// handleAgentReport: POST /projects/{id}/agent/tasks/{task}/report — publish a live
// develop/verify event (progress/log/diff) on the shared bus for the editor.
func (s *apiServer) handleAgentReport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	taskID := r.PathValue("task")

	var req agentReportRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	ev := events.Event{Project: id, Task: taskID, Phase: events.Phase(req.Phase), Kind: events.Kind(req.Kind), Payload: req.Payload}
	if err := ev.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid event: "+err.Error())
		return
	}
	if s.bus != nil {
		if err := s.bus.Publish(r.Context(), ev); err != nil {
			s.serverError(w, "agent report: publish", err)
			return
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"published": s.bus != nil})
}

// handleAgentResult: POST /projects/{id}/agent/tasks/{task}/result — record the
// agent's verdict and decide what happens next. This is the gateway-mediated analogue
// of the daemon's verify→governance step. Honest, never-fake-green:
//
//   - gate not passed                → markBlocked, decision "blocked"
//   - passed + held-for-review/T3-T4 → park awaiting-approval (record branch), "hold"
//   - passed + auto-merge tier       → decision "merge" (the agent merges, posts /merged)
//
// The merge itself happens on the AGENT (it holds the worktree + git creds); the
// gateway only records state and emits the verdict for the editor.
func (s *apiServer) handleAgentResult(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	taskID := r.PathValue("task")

	var req agentResultRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	result := strings.ToLower(strings.TrimSpace(req.Result))
	switch result {
	case "pass", "changes-requested", "blocked":
	default:
		writeError(w, http.StatusBadRequest, "result must be pass, changes-requested, or blocked")
		return
	}

	ctx := r.Context()
	project, err := s.store.GetProject(ctx, id)
	if err != nil {
		if errors.Is(err, statestore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		s.serverError(w, "agent result: get project", err)
		return
	}
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil || task.ProjectID != id {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	// Emit the verdict for the editor (KindDecision at PhaseReview).
	checks := make([]map[string]any, 0, len(req.Checks))
	for _, c := range req.Checks {
		checks = append(checks, map[string]any{"name": c.Name, "result": c.Result, "evidence": c.Evidence})
	}
	s.publishAgentEvent(ctx, id, taskID, events.PhaseReview, events.KindDecision,
		map[string]any{"result": result, "summary": req.Summary, "checks": checks})

	if result != "pass" {
		// Never fake-green: a failed gate blocks the task (re-runnable), never done.
		if err := s.updateTask(ctx, taskID, func(t *statestore.Task) { t.Status = "blocked" }); err != nil {
			s.serverError(w, "agent result: mark blocked", err)
			return
		}
		writeJSON(w, http.StatusOK, agentResultResponse{Decision: "blocked"})
		return
	}

	// Passed: consult the merge policy.
	if policyForProject(project).MergeMode(task).HumanRequired() {
		if strings.TrimSpace(req.Branch) == "" {
			writeError(w, http.StatusBadRequest, "branch is required to hold a passed task for approval")
			return
		}
		// Park awaiting-approval, preserving the verified branch (no re-develop on approve).
		if err := s.updateTask(ctx, taskID, func(t *statestore.Task) {
			t.Status = "awaiting-approval"
			t.Branch = req.Branch
			t.Approved = false
		}); err != nil {
			s.serverError(w, "agent result: hold for approval", err)
			return
		}
		s.publishAgentEvent(ctx, id, taskID, events.PhaseReview, events.KindInterventionNeeded,
			map[string]any{"reason": "held for review", "tier": task.Tier})
		writeJSON(w, http.StatusOK, agentResultResponse{Decision: "hold"})
		return
	}

	// Auto-merge tier: the agent may merge now.
	writeJSON(w, http.StatusOK, agentResultResponse{Decision: "merge"})
}

// handleAgentDecision: GET /projects/{id}/agent/tasks/{task}/decision — the held-task
// decision the agent polls for. approved (director ran /approve) → the agent merges;
// aborted (director ran /abort) → the agent stops; pending → keep waiting.
func (s *apiServer) handleAgentDecision(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	taskID := r.PathValue("task")

	task, err := s.store.GetTask(r.Context(), taskID)
	if err != nil || task.ProjectID != id {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	state := "pending"
	switch {
	case task.AbortRequested:
		state = "aborted"
	case task.Status == "awaiting-approval" && task.Approved:
		state = "approved"
	}
	writeJSON(w, http.StatusOK, agentDecisionResponse{State: state})
}

// handleAgentMerged: POST /projects/{id}/agent/tasks/{task}/merged — the agent landed
// the merge (auto or approved); mark the task done (terminal) and clear the approval
// signal, then emit the merge event for the editor.
func (s *apiServer) handleAgentMerged(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	taskID := r.PathValue("task")

	var req agentMergedRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if strings.TrimSpace(req.SHA) == "" {
		writeError(w, http.StatusBadRequest, "sha is required")
		return
	}

	ctx := r.Context()
	project, err := s.store.GetProject(ctx, id)
	if err != nil {
		if errors.Is(err, statestore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		s.serverError(w, "agent merged: get project", err)
		return
	}
	task, err := s.store.GetTask(ctx, taskID)
	if err != nil || task.ProjectID != id {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}

	if err := s.updateTask(ctx, taskID, func(t *statestore.Task) {
		t.Status = "done"
		t.Approved = false
	}); err != nil {
		s.serverError(w, "agent merged: mark done", err)
		return
	}
	s.publishAgentEvent(ctx, id, taskID, events.PhaseMerge, events.KindMerge,
		map[string]any{"sha": req.SHA, "base": project.BaseBranch})
	writeJSON(w, http.StatusOK, map[string]any{"status": "done"})
}

// ─────────────────────────────────────────────────────────────────────────────────────
// L3 (ADR-0049) — the gateway-distributed encrypted credential store. The editor uploads
// the director's portable claude OAuth token once (PUT), the gateway SEALS it
// (credstore AES-256-GCM, master key from a k8s secret) before it touches Postgres, each
// agent fetches the decrypted token at startup (GET) over this authed channel, and logout
// removes it (DELETE). Fail-closed: with no master key configured (s.sealer == nil) the
// PUT/GET endpoints return 503 rather than ever storing/serving plaintext.
//
// TOKEN DISCIPLINE: the token appears ONLY in the request/response body over the authed
// channel; it is NEVER logged (serverError logs an op + a shape-only error, and credstore
// errors carry no key/nonce/plaintext). PUT/DELETE return no body; GET returns the token
// only to an authed caller.
// ─────────────────────────────────────────────────────────────────────────────────────

// credentialPutRequest is the body of PUT /agent/credentials/{kind}: the plaintext token to
// seal. It is sealed immediately; it is never persisted in the clear or logged.
type credentialPutRequest struct {
	Token string `json:"token"`
}

// credentialGetResponse is the body of GET /agent/credentials/{kind}: the decrypted token,
// returned only to an authed caller over the authed channel (and never logged).
type credentialGetResponse struct {
	Token string `json:"token"`
}

// credStore type-asserts the optional CredentialStore seam, returning false (→ caller writes
// 501) when the configured store has no credential persistence.
func (s *apiServer) credStore() (statestore.CredentialStore, bool) {
	cs, ok := s.store.(statestore.CredentialStore)
	return cs, ok
}

// handleAgentPutCredential: PUT /agent/credentials/{kind} — seal + upsert the uploaded token.
// 204 on success; 400 empty kind/token; 501 no credential store; 503 no master key (fail-closed).
func (s *apiServer) handleAgentPutCredential(w http.ResponseWriter, r *http.Request) {
	kind := strings.TrimSpace(r.PathValue("kind"))
	if kind == "" {
		writeError(w, http.StatusBadRequest, "kind is required")
		return
	}
	cs, ok := s.credStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "credential storage not configured")
		return
	}
	if s.sealer == nil {
		writeError(w, http.StatusServiceUnavailable, "credential encryption not configured")
		return
	}
	var req credentialPutRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Token) == "" {
		writeError(w, http.StatusBadRequest, "token is required")
		return
	}
	ciphertext, nonce, err := s.sealer.Seal([]byte(req.Token))
	if err != nil {
		s.serverError(w, "credential: seal", err)
		return
	}
	if err := cs.PutCredential(r.Context(), statestore.Credential{Kind: kind, Ciphertext: ciphertext, Nonce: nonce}); err != nil {
		s.serverError(w, "credential: put", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAgentGetCredential: GET /agent/credentials/{kind} — fetch + decrypt the token for an
// authed caller (the agent). 200 {token}; 404 none stored; 501 no store; 503 no master key.
func (s *apiServer) handleAgentGetCredential(w http.ResponseWriter, r *http.Request) {
	kind := strings.TrimSpace(r.PathValue("kind"))
	if kind == "" {
		writeError(w, http.StatusBadRequest, "kind is required")
		return
	}
	cs, ok := s.credStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "credential storage not configured")
		return
	}
	if s.sealer == nil {
		writeError(w, http.StatusServiceUnavailable, "credential encryption not configured")
		return
	}
	c, err := cs.GetCredential(r.Context(), kind)
	if errors.Is(err, statestore.ErrNotFound) {
		writeError(w, http.StatusNotFound, "credential not found")
		return
	}
	if err != nil {
		s.serverError(w, "credential: get", err)
		return
	}
	token, err := s.sealer.Open(c.Ciphertext, c.Nonce)
	if err != nil {
		// Decrypt failed (e.g. the master key was rotated away from the one that sealed this
		// row). Shape-only error; never log the ciphertext/nonce/token.
		s.serverError(w, "credential: open", err)
		return
	}
	writeJSON(w, http.StatusOK, credentialGetResponse{Token: string(token)})
}

// handleAgentDeleteCredential: DELETE /agent/credentials/{kind} — forget the credential
// (logout propagation). 204; idempotent (deleting an absent kind succeeds). No master key
// needed (delete does not decrypt). 501 when no credential store is configured.
func (s *apiServer) handleAgentDeleteCredential(w http.ResponseWriter, r *http.Request) {
	kind := strings.TrimSpace(r.PathValue("kind"))
	if kind == "" {
		writeError(w, http.StatusBadRequest, "kind is required")
		return
	}
	cs, ok := s.credStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "credential storage not configured")
		return
	}
	if err := cs.DeleteCredential(r.Context(), kind); err != nil {
		s.serverError(w, "credential: delete", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
