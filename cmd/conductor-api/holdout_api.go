// Faz-S holdout store endpoints (S2). PUT /holdouts/{id} persists an intake-approved holdout body
// (the editor calls this on approve, S5); GET /agent/holdout?ref= serves it to the agent's verify
// gate (S3). Both are authed (requireAuth wraps them in routes()). The holdout BODY is repo-external
// (central Postgres) and is NEVER logged (ADR-0018). When no holdout store is configured (no DSN)
// both fail with 501 rather than panicking — the same OPTIONAL-seam pattern as the distiller.
//
// Wire format: file contents are base64 in JSON so arbitrary bytes (binary fixtures, CRLF) survive
// a JSON round-trip intact. PUT body: {"files":{"<path>":"<base64>"}}. GET response:
// {"name":"<id>","files":{"<path>":"<base64>"}}.
package main

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strings"

	"github.com/everva/conductor-platform/internal/statestore"
)

// holdoutFilesRequest is the PUT body: a map of worktree-relative path -> base64 file content.
type holdoutFilesRequest struct {
	Files map[string]string `json:"files"`
}

// holdoutFilesResponse is the GET response: the holdout id + its files (base64 content).
type holdoutFilesResponse struct {
	Name  string            `json:"name"`
	Files map[string]string `json:"files"`
}

// holdoutLocatorResponse is the PUT response: the stored holdout's pg:// locator (what a
// scenario's hidden_holdout_ref should be set to).
type holdoutLocatorResponse struct {
	Locator string `json:"locator"`
}

// handlePutHoldout: PUT /holdouts/{id} — store an approved holdout body, return its locator.
// 201 {locator}; 400 bad body / empty id / empty files / undecodable base64; 501 no store
// configured; 500 on a store error. The body is never logged.
func (s *apiServer) handlePutHoldout(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "holdout id is required")
		return
	}
	if s.holdouts == nil {
		writeError(w, http.StatusNotImplemented, "holdout store not configured")
		return
	}
	var req holdoutFilesRequest
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return
	}
	if len(req.Files) == 0 {
		writeError(w, http.StatusBadRequest, "at least one file is required")
		return
	}
	// Decode each base64 body. A bad encoding is a 400 (client error), never a 500.
	files := make(map[string][]byte, len(req.Files))
	for path, b64 := range req.Files {
		content, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "file content must be base64")
			return
		}
		files[path] = content
	}
	locator, err := s.holdouts.Store(r.Context(), id, files)
	if err != nil {
		// Store validates id/path (safeRel) too; a validation reason is descriptive + secret-free,
		// so surface it as a 400. Anything else is an internal error (logged, generic to client).
		if isHoldoutValidationErr(err) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.serverError(w, "holdout: store", err)
		return
	}
	writeJSON(w, http.StatusCreated, holdoutLocatorResponse{Locator: locator})
}

// handleGetHoldout: GET /agent/holdout?ref=<pg://...> — fetch a holdout body for the agent's gate.
// 200 {name,files}; 400 missing ref; 404 not found; 501 no store; 500 other. Never logs the body.
func (s *apiServer) handleGetHoldout(w http.ResponseWriter, r *http.Request) {
	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	if ref == "" {
		writeError(w, http.StatusBadRequest, "ref query parameter is required")
		return
	}
	if s.holdouts == nil {
		writeError(w, http.StatusNotImplemented, "holdout store not configured")
		return
	}
	h, err := s.holdouts.Fetch(r.Context(), ref)
	if err != nil {
		if errors.Is(err, statestore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "holdout not found")
			return
		}
		// A missing pg holdout surfaces as a descriptive "no rows for id" error (not ErrNotFound);
		// treat any fetch failure as 404 so a caller can't distinguish "absent" from "store error"
		// for an arbitrary ref (no information leak), while the real cause is logged server-side.
		s.logFetchMiss(r, ref, err)
		writeError(w, http.StatusNotFound, "holdout not found")
		return
	}
	files := make(map[string]string, len(h.Files))
	for path, content := range h.Files {
		files[path] = base64.StdEncoding.EncodeToString(content)
	}
	writeJSON(w, http.StatusOK, holdoutFilesResponse{Name: h.Name, Files: files})
}

// logFetchMiss records the REAL fetch error server-side WITHOUT the holdout body (ADR-0018) so an
// operator can diagnose a 404 while the client only ever sees the fixed "not found".
func (s *apiServer) logFetchMiss(r *http.Request, ref string, err error) {
	if s.logger != nil {
		s.logger.WarnContext(r.Context(), "holdout: fetch miss", "ref", ref, "err", err.Error())
	}
}

// isHoldoutValidationErr reports whether a Store error is a client-fixable validation reason
// (empty id/files, unsafe path) rather than an internal failure — those messages are descriptive
// and secret-free, so they can be returned to the caller as a 400.
func isHoldoutValidationErr(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "unsafe path") ||
		strings.Contains(msg, "non-empty id") ||
		strings.Contains(msg, "no files") ||
		strings.Contains(msg, "empty file path")
}
