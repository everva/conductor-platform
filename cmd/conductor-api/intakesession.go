package main

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/everva/conductor-platform/internal/statestore"
)

// Intake CONVERSATION history (director-facing). The intake chat used to be ephemeral; the director
// asked to see prior conversations per project (Claude-Code-style). The web now UPSERTS each
// conversation (PUT) and lists/opens them (GET). ADDITIVE (ADR-0021): reached by an optional
// IntakeSessionStore type-assertion; 501 when the store lacks it.
//
// TOKEN/SECRET DISCIPLINE: only the director's own conversation text crosses these endpoints — no
// gateway/claude tokens. Persistence is the OPT-IN history the director chose.

type intakeMessageDTO struct {
	Role string `json:"role"`
	Text string `json:"text"`
	Tone string `json:"tone,omitempty"`
}

// putIntakeSessionRequest is the director's PUT body: a snapshot of the whole conversation.
type putIntakeSessionRequest struct {
	Title    string             `json:"title"`
	Messages []intakeMessageDTO `json:"messages"`
	Result   string             `json:"result,omitempty"`
	// CreatedAt (optional RFC3339) lets the web stamp the conversation's start; the store keeps it
	// only on the FIRST save and preserves it across later saves.
	CreatedAt string `json:"created_at,omitempty"`
}

// intakeSessionSummaryDTO is one row of the history list — light (no Messages); has_result flags a
// conversation that produced a draft so the UI can mark it.
type intakeSessionSummaryDTO struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Title     string `json:"title"`
	HasResult bool   `json:"has_result"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// intakeSessionDTO is the FULL conversation (Get), with the message thread.
type intakeSessionDTO struct {
	ID        string             `json:"id"`
	ProjectID string             `json:"project_id"`
	Title     string             `json:"title"`
	Messages  []intakeMessageDTO `json:"messages"`
	Result    string             `json:"result,omitempty"`
	CreatedAt string             `json:"created_at"`
	UpdatedAt string             `json:"updated_at"`
}

// intakeSessionStore returns the optional IntakeSessionStore seam, or false (→ 501) when the
// configured store does not implement it.
func (s *apiServer) intakeSessionStore() (statestore.IntakeSessionStore, bool) {
	is, ok := s.store.(statestore.IntakeSessionStore)
	return is, ok
}

func messagesFromDTO(in []intakeMessageDTO) []statestore.IntakeMessage {
	if len(in) == 0 {
		return nil
	}
	out := make([]statestore.IntakeMessage, len(in))
	for i, m := range in {
		out[i] = statestore.IntakeMessage{Role: m.Role, Text: m.Text, Tone: m.Tone}
	}
	return out
}

func messagesToDTO(in []statestore.IntakeMessage) []intakeMessageDTO {
	out := make([]intakeMessageDTO, 0, len(in))
	for _, m := range in {
		out = append(out, intakeMessageDTO{Role: m.Role, Text: m.Text, Tone: m.Tone})
	}
	return out
}

// handlePutIntakeSession: PUT /projects/{id}/intake/sessions/{sid} — upsert a conversation.
func (s *apiServer) handlePutIntakeSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sid := r.PathValue("sid")
	is, ok := s.intakeSessionStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "intake session store not configured")
		return
	}
	if strings.TrimSpace(sid) == "" {
		writeError(w, http.StatusBadRequest, "session id is required")
		return
	}
	var req putIntakeSessionRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	ctx := r.Context()
	// Persist only into a REAL project (existence check; no mutation of project state).
	if _, err := s.store.GetProject(ctx, id); err != nil {
		if errors.Is(err, statestore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		s.serverError(w, "intake session: get project", err)
		return
	}
	sess := statestore.IntakeSession{
		ID:        sid,
		ProjectID: id,
		Title:     req.Title,
		Messages:  messagesFromDTO(req.Messages),
		Result:    req.Result,
	}
	if req.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339, req.CreatedAt); err == nil {
			sess.CreatedAt = t
		}
	}
	if err := is.PutIntakeSession(ctx, sess); err != nil {
		if errors.Is(err, statestore.ErrInvalid) {
			writeError(w, http.StatusBadRequest, "invalid session")
			return
		}
		s.serverError(w, "intake session: put", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "id": sid})
}

// handleListIntakeSessions: GET /projects/{id}/intake/sessions — the project's conversations,
// newest-first, as light summaries (no message bodies).
func (s *apiServer) handleListIntakeSessions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	is, ok := s.intakeSessionStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "intake session store not configured")
		return
	}
	sessions, err := is.ListIntakeSessions(r.Context(), id)
	if err != nil {
		s.serverError(w, "intake session: list", err)
		return
	}
	out := make([]intakeSessionSummaryDTO, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, intakeSessionSummaryDTO{
			ID:        sess.ID,
			ProjectID: sess.ProjectID,
			Title:     sess.Title,
			HasResult: strings.TrimSpace(sess.Result) != "",
			CreatedAt: sess.CreatedAt.UTC().Format(time.RFC3339Nano),
			UpdatedAt: sess.UpdatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// handleGetIntakeSession: GET /projects/{id}/intake/sessions/{sid} — the full conversation thread.
func (s *apiServer) handleGetIntakeSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sid := r.PathValue("sid")
	is, ok := s.intakeSessionStore()
	if !ok {
		writeError(w, http.StatusNotImplemented, "intake session store not configured")
		return
	}
	// Guard the (project, session) pairing so one project can't read another's conversation.
	sess, err := is.GetIntakeSession(r.Context(), sid)
	if err != nil || sess.ProjectID != id {
		writeError(w, http.StatusNotFound, "intake session not found")
		return
	}
	writeJSON(w, http.StatusOK, intakeSessionDTO{
		ID:        sess.ID,
		ProjectID: sess.ProjectID,
		Title:     sess.Title,
		Messages:  messagesToDTO(sess.Messages),
		Result:    sess.Result,
		CreatedAt: sess.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt: sess.UpdatedAt.UTC().Format(time.RFC3339Nano),
	})
}
