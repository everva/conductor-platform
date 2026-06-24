package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"github.com/everva/conductor-platform/internal/statestore"
)

// Intake ENHANCE (agent-side, code-aware). The director writes a rough request in intake and
// clicks Enhance; the gateway records a pending job; an AGENT (which has the project's repo
// cloned + claude) claims it, runs claude read-only over the real code, and writes back a
// detailed Turkish spec; the editor polls and fills the composer. ADDITIVE (ADR-0021): reached
// by an optional EnhanceStore type-assertion; 501 when the store lacks it.
//
// TOKEN/SECRET DISCIPLINE: only the rough request + the produced spec cross these endpoints —
// no gateway/claude tokens. The agent's claude auth stays on the agent (L3), never here.

type enhanceRequest struct {
	RoughSpec string `json:"rough_spec"`
}

type enhanceJobResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// agentEnhanceClaim is the body the agent gets from GET /agent/enhance/next (204 when none).
type agentEnhanceClaim struct {
	ID        string `json:"id"`
	RoughSpec string `json:"rough_spec"`
}

// agentEnhanceResult is the agent's POST body for a finished enhance.
type agentEnhanceResult struct {
	Result string `json:"result"`
	Error  string `json:"error"`
}

// newEnhanceID returns a short, collision-free job id.
func newEnhanceID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "enh-" + hex.EncodeToString(b[:])
}

// enhanceStore returns the optional EnhanceStore seam, or false (→ 501) when the configured
// store does not implement it.
func (s *apiServer) enhanceStore() (statestore.EnhanceStore, bool) {
	es, ok := s.store.(statestore.EnhanceStore)
	return es, ok
}

// handleEnhance: POST /projects/{id}/enhance — record a pending enhance job for an agent to run.
func (s *apiServer) handleEnhance(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	es, ok := s.enhanceStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "enhance store not configured")
		return
	}
	var req enhanceRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if strings.TrimSpace(req.RoughSpec) == "" {
		writeError(w, http.StatusBadRequest, "rough_spec is required")
		return
	}
	ctx := r.Context()
	if _, err := s.store.GetProject(ctx, id); err != nil {
		if errors.Is(err, statestore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		s.serverError(w, "enhance: get project", err)
		return
	}
	job := statestore.EnhanceJob{ID: newEnhanceID(), ProjectID: id, RoughSpec: req.RoughSpec}
	if err := es.CreateEnhanceJob(ctx, job); err != nil {
		s.serverError(w, "enhance: create job", err)
		return
	}
	writeJSON(w, http.StatusOK, enhanceJobResponse{ID: job.ID, Status: statestore.EnhancePending})
}

// handleEnhanceGet: GET /projects/{id}/enhance/{job} — poll an enhance job's status/result.
func (s *apiServer) handleEnhanceGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	jobID := r.PathValue("job")
	es, ok := s.enhanceStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "enhance store not configured")
		return
	}
	job, err := es.GetEnhanceJob(r.Context(), jobID)
	if err != nil || job.ProjectID != id {
		writeError(w, http.StatusNotFound, "enhance job not found")
		return
	}
	writeJSON(w, http.StatusOK, enhanceJobResponse{ID: job.ID, Status: job.Status, Result: job.Result, Error: job.Error})
}

// handleAgentEnhanceNext: GET /projects/{id}/agent/enhance/next — claim the oldest pending
// enhance job (→ running). 204 when none pending.
func (s *apiServer) handleAgentEnhanceNext(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	es, ok := s.enhanceStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "enhance store not configured")
		return
	}
	job, claimed, err := es.ClaimEnhanceJob(r.Context(), id)
	if err != nil {
		s.serverError(w, "enhance: claim job", err)
		return
	}
	if !claimed {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, agentEnhanceClaim{ID: job.ID, RoughSpec: job.RoughSpec})
}

// handleAgentEnhanceResult: POST /projects/{id}/agent/enhance/{job}/result — the agent writes
// back the enhanced spec (or an error).
func (s *apiServer) handleAgentEnhanceResult(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	jobID := r.PathValue("job")
	es, ok := s.enhanceStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "enhance store not configured")
		return
	}
	var req agentEnhanceResult
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	// Guard the (project, job) pairing so an agent can't complete another project's job.
	job, err := es.GetEnhanceJob(r.Context(), jobID)
	if err != nil || job.ProjectID != id {
		writeError(w, http.StatusNotFound, "enhance job not found")
		return
	}
	if err := es.CompleteEnhanceJob(r.Context(), jobID, req.Result, req.Error); err != nil {
		s.serverError(w, "enhance: complete job", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}
