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
	"errors"
	"net/http"
	"strings"
	"time"

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
