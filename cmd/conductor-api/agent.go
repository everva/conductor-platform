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

// reviewFindingPrefix marks the acceptance lines that carry a gate's unresolved findings
// rather than the scenario's own spec. It is BOTH the text handed to the developer and the
// key the store matches on to supersede the previous round's findings — one const so the
// writer and the pruner can never drift apart and orphan a line forever.
const reviewFindingPrefix = "Resolve this prior-review finding before merge: "

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
		Reason:         t.LastError,
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
	proj, err := s.store.GetProject(ctx, id)
	if err != nil {
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

	// Honor Project.Paused on the host-agent lease path (ADR-0003): a paused project leases
	// NO task — the agent gets 204 "no work" and idles until /resume, WITHOUT a manual drain.
	// Placed AFTER host registration so the host's liveness heartbeat still updates while
	// paused (it is not reaped as dead). Without this the pause was cosmetic on the gateway-
	// mediated path — the agent kept leasing and a "paused" project kept building (observed:
	// a project built for days through a pause because only the in-process daemon Tick, which
	// this deployment does not run, honored the flag).
	if proj.Paused {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Free any task stranded in "running" with nothing behind it, BEFORE picking. This runs on
	// every poll — including the polls that end in 204 "no work" — because a project whose every
	// task is stranded never reaches PickReady at all, and a sweep placed after a successful pick
	// could therefore never save it (see reapPhantomRunners).
	s.reapPhantomRunners(ctx, id)

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

	// Advance the leased task to "running" so the STORED status matches the live lease
	// (M1). The board derives "running" from the active lease, but /tasks readers — the
	// editor's sessions tree — show Task.Status, which previously stayed todo/ready while
	// the task was actually executing, producing a tree-vs-board mismatch. The lease record
	// already holds the host; here we only advance the lifecycle status. A release with no
	// terminal verdict reverts it (see handleAgentReleaseLease). Best-effort: if this write
	// fails the lease is still held and the reaper will free it, so log + still return the
	// task rather than fabricating a 500 the agent can't act on.
	// A fresh attempt clears any stale LastError from a prior block, so the board shows the task
	// running clean rather than "running" next to a now-irrelevant failure reason.
	//
	// EXCEPTION: an APPROVED awaiting-approval task keeps its status. PickReady hands it back so
	// a NEW agent process can finish the merge its predecessor was polling for (the approval is a
	// durable flag, not a message), and the agent decides "merge, don't develop" from exactly that
	// status. Overwriting it with "running" would erase the only signal distinguishing verified
	// work awaiting a merge from work that still needs developing.
	if task.Status != registry.StatusAwaitingApproval {
		if err := s.updateTask(ctx, task.ID, func(t *statestore.Task) { t.Status = registry.StatusRunning; t.LastError = "" }); err != nil {
			s.serverError(w, "agent lease: mark task running", err)
			return
		}
		task.Status = registry.StatusRunning
		task.LastError = ""
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

	// Capture ownership BEFORE releasing: only the lease OWNER should revert the task's
	// running status. A non-owner release is an idempotent no-op (the lease stays), so it
	// must NOT touch the status of a task another host is actively running.
	//
	// A MISSING lease ALSO authorizes the revert. The reaper TTL (ADR-0016) is measured on
	// AcquiredAt — the age of the WORK, not of any liveness signal — so a develop that runs
	// longer than the TTL (host-agents use -timeout 45m/60m against the 30m default) has its
	// lease reaped out from under a perfectly healthy agent. When that agent later releases
	// without a verdict, the ownership check fails, the revert below is skipped, and the task
	// is stranded "running" forever: no holder, and no API can recover it (/retry takes only
	// blocked, /abort needs a lease). Observed live as 4 phantom tasks across three projects.
	// With NO lease present nobody owns the repo, so a still-running task can only belong to
	// the releasing host — reverting is strictly safer than stranding it.
	owned, leaseMissing := false, false
	if l, err := s.store.GetLease(r.Context(), id); err == nil {
		owned = l.HostID == req.HostID && l.TaskID == req.TaskID
	} else if errors.Is(err, statestore.ErrNotFound) {
		leaseMissing = true
	}

	if err := reg.ReleaseLeaseOwned(r.Context(), id, req.HostID, req.TaskID); err != nil {
		s.serverError(w, "agent release lease", err)
		return
	}

	// Revert the task to "ready" when the releaser could legitimately have been running it
	// (M1): it released without a terminal verdict — a crash, merge conflict, or abandon — so
	// the lease→running flip would otherwise strand it as permanently "running" with no holder.
	// A task already moved to blocked / awaiting-approval / done by the result/merge path is
	// left untouched. A missing task (idempotent release after the task was deleted) is a
	// clean no-op.
	if owned || leaseMissing {
		if err := s.updateTask(r.Context(), req.TaskID, func(t *statestore.Task) {
			if t.Status == registry.StatusRunning {
				t.Status = registry.StatusReady
			}
		}); err != nil && !errors.Is(err, statestore.ErrNotFound) {
			s.serverError(w, "agent release lease: revert status", err)
			return
		}
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
	Result   string          `json:"result"` // pass | changes-requested | blocked
	Branch   string          `json:"branch"`
	Summary  string          `json:"summary"`
	Checks   []agentCheckDTO `json:"checks"`
	Findings []string        `json:"findings,omitempty"` // unresolved reviewer/gate findings to persist onto the scenario acceptance
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

// reapPhantomRunners reverts every task stranded in "running" with nothing behind it.
//
// THE INVARIANT: a lease is held per PROJECT (GetLease takes the project id), so at most ONE task
// of a project can legitimately be running — the leased one. Any OTHER task sitting in "running" is
// a phantom: its worker is gone and its lease is not.
//
// Why the phantom is otherwise unrecoverable. /retry takes only a blocked task (409 otherwise).
// /agent/lease/release reverts the status only for the lease OWNER or when NO lease exists — so a
// phantom that coexists with a live lease on a DIFFERENT task is a no-op there, and the free-lease
// window between two tasks is sub-second, so no client can catch it by polling. The reconcile reaper
// that would have cleaned this up (ADR-0016) lives in cmd/conductor, which THIS deployment does not
// run: the gateway is the whole control plane. So nothing, anywhere, frees them.
//
// Observed live (2026-07-12): xirigo-vendor sat silent for 14 hours. V-21 and V-39 were stranded
// "running" from an agent restart; V-40's dependency was V-39, so PickReady had nothing to give and
// the agent polled into an empty room. The project looked "working" on the board — two tasks running
// — while not a single process existed behind them. A phantom is worse than a blocked task: it
// reports progress it is not making, and it takes its dependents down with it.
//
// The sweep is idempotent, needs no TTL or heartbeat, and cannot touch a live task: the leased task
// is skipped by ID, and a task can only be running-and-unleased if nobody is running it.
func (s *apiServer) reapPhantomRunners(ctx context.Context, projectID string) {
	leasedTaskID := ""
	if l, err := s.store.GetLease(ctx, projectID); err == nil {
		leasedTaskID = l.TaskID
	} else if !errors.Is(err, statestore.ErrNotFound) {
		// Store trouble: leave the state alone rather than guess which task is live.
		return
	}

	tasks, err := s.store.ListTasks(ctx, projectID)
	if err != nil {
		return
	}
	for _, t := range tasks {
		if t.Status != registry.StatusRunning || t.ID == leasedTaskID {
			continue
		}
		id := t.ID
		if err := s.updateTask(ctx, id, func(t *statestore.Task) {
			// Re-check under the read-modify-write: a lease may have been acquired in between.
			if t.Status == registry.StatusRunning {
				t.Status = registry.StatusReady
			}
		}); err != nil {
			if s.logger != nil {
				s.logger.WarnContext(ctx, "agent lease: could not revert phantom running task",
					"project", projectID, "task", id, "err", err)
			}
			continue
		}
		if s.logger != nil {
			s.logger.InfoContext(ctx, "agent lease: reverted a phantom running task (running, but no lease behind it)",
				"project", projectID, "task", id)
		}
	}
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

// agentTaskDiffRequest is the body of POST /projects/{id}/agent/tasks/{task}/diff: the full-file
// patch the agent computed over its worktree (Faz-S review parity), stored so GET .../diff serves
// the native side-by-side diff for review.
type agentTaskDiffRequest struct {
	Base      string `json:"base"`
	Branch    string `json:"branch"`
	Patch     string `json:"patch"`
	Truncated bool   `json:"truncated"`
}

// handleAgentTaskDiff: POST /projects/{id}/agent/tasks/{task}/diff — store the agent's full-file
// diff so the editor's native diff (GET .../tasks/{task}/diff) works. 204 on success; 400 bad body;
// 501 when no diff storage is configured (memory dev) — the bounded KindDiff event still flows, so
// the Session view's inline diff works even without the full-file store. The patch is never logged.
func (s *apiServer) handleAgentTaskDiff(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	taskID := r.PathValue("task")
	var req agentTaskDiffRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	tds, ok := s.store.(statestore.TaskDiffStore)
	if !ok {
		writeError(w, http.StatusNotImplemented, "diff storage not configured")
		return
	}
	if err := tds.PutTaskDiff(r.Context(), statestore.TaskDiff{
		ProjectID: id, TaskID: taskID,
		Base: req.Base, Branch: req.Branch, Patch: req.Patch, Truncated: req.Truncated,
	}); err != nil {
		s.serverError(w, "agent task diff: put", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
		// Never fake-green: a failed gate blocks the task (re-runnable), never done. Persist the
		// agent's Summary as the task's LastError so the director's board explains WHY it stalled
		// ("…malformed verdict…", "gate unresolved — needs user") instead of a bare "blocked".
		reason := strings.TrimSpace(req.Summary)
		if err := s.updateTask(ctx, taskID, func(t *statestore.Task) { t.Status = "blocked"; t.LastError = reason }); err != nil {
			s.serverError(w, "agent result: mark blocked", err)
			return
		}
		// PERSIST the unresolved findings onto the scenario's acceptance so a later re-develop —
		// a fresh worktree that loses .conductor/REVIEW.md — still addresses them and the reviewer
		// re-checks them. This is what lets a DENSE screen CONVERGE across autoheal retries instead
		// of cycling forever.
		//
		// REPLACE, never append: findings describe the LATEST gate run. Appending accumulated every
		// round forever, so one bad finding stuck to the scenario permanently — and a bad one DID
		// stick (the gate reported its banner, "verify: node v22.23.0 / npm 10.9.8", as the failure),
		// leaving 30 scenarios ordering the developer to "resolve" a version string. With nothing
		// actionable to do it changed nothing and the task blocked as "developer made no change".
		// Replacing supersedes such a line on the next report instead of carrying it for life.
		//
		// Best-effort: a persist failure must NOT fail the block report.
		if task.ScenarioID != "" {
			crit := make([]string, 0, len(req.Findings))
			for _, f := range req.Findings {
				if f = strings.TrimSpace(f); f != "" {
					crit = append(crit, reviewFindingPrefix+f)
				}
			}
			if err := s.store.ReplaceScenarioFindings(ctx, task.ScenarioID, reviewFindingPrefix, crit); err != nil && s.logger != nil {
				s.logger.WarnContext(ctx, "agent result: persist review findings to acceptance failed",
					"task", taskID, "scenario", task.ScenarioID, "err", err.Error())
			}
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
