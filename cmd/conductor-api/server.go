// HTTP surface for the conductor-api read gateway (3A-1): the router, the bearer
// auth middleware, the read handlers, and the health probes. It is split from
// main.go's lifecycle so tests can drive routes() with httptest against an
// in-memory store — no socket, no Postgres, no real clock.
//
// Every handler is read-only over the FROZEN statestore.StateStore and emits
// secret-free JSON (never the DSN or token). Collections always serialize as a
// JSON array ("[]" when empty, never null) by allocating the slice with a zero
// length. Time-derived fields (heartbeat age, generated_at) come from an
// injectable clock so tests are deterministic.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/everva/conductor-platform/internal/events"
	"github.com/everva/conductor-platform/internal/intake"
	"github.com/everva/conductor-platform/internal/statestore"
)

// readinessTimeout bounds the store read the /readyz probe performs so a hung
// backend turns into a fast 503 rather than blocking the probe (and the k8s
// readiness gate) indefinitely. Mirrors the daemon's httpserver.go.
const readinessTimeout = 2 * time.Second

// apiServer holds the dependencies the handlers need. It is constructed in
// main.go (real store + token + time.Now) and in tests (in-memory store + fixed
// token + fixed clock), then exposes an http.Handler via routes().
type apiServer struct {
	// store is the SHARED, FROZEN statestore — read-only here.
	store statestore.StateStore
	// bus is the event bus the daemon publishes lifecycle events on; /ws
	// subscribes to it for realtime push. The same backend (memory/PG) the daemon
	// uses, selected by DSN in main.
	bus events.EventBus
	// reader is the additive historical-replay seam backing GET /events. In
	// practice the SAME concrete value as bus (both real impls satisfy both
	// interfaces); it is nil only if a bus without EventReader is configured, in
	// which case GET /events returns 501 rather than panicking.
	reader events.EventReader
	// token is the expected bearer token (constant-time compared, never echoed).
	token string
	// distiller is the assisted-distillation seam (ADR-0005, ADR-0012) backing
	// POST /projects/{id}/distill: it turns a free-text conversation into PROPOSED,
	// shape-validated scenarios for human review (never persists). main.go wires the
	// production intake.NewCommandDistiller (claude -p); tests inject a stub. It is
	// drafting-only — when nil the distill endpoint returns 501 rather than panicking.
	distiller intake.Distiller
	// clock is the injectable time source for heartbeat-age and generated_at, so
	// time-derived JSON is deterministic in tests. Defaults to time.Now in main.
	clock func() time.Time
}

// now returns the current time via the injected clock, defaulting to time.Now so
// a zero-value apiServer (or one constructed without a clock) still works.
func (s *apiServer) now() time.Time {
	if s.clock == nil {
		return time.Now()
	}
	return s.clock()
}

// routes builds the gateway mux. /healthz and /readyz are UNAUTHENTICATED (k8s
// probes); every other route is wrapped by requireAuth. Go 1.22+ method+pattern
// routing with path wildcards is used (go.mod is go 1.26).
func (s *apiServer) routes() http.Handler {
	mux := http.NewServeMux()

	// Unauthenticated probes (k8s liveness/readiness).
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)

	// Protected read endpoints.
	mux.Handle("GET /projects", s.requireAuth(http.HandlerFunc(s.handleProjects)))
	mux.Handle("GET /projects/{id}/tasks", s.requireAuth(http.HandlerFunc(s.handleProjectTasks)))
	mux.Handle("GET /hosts", s.requireAuth(http.HandlerFunc(s.handleHosts)))
	mux.Handle("GET /status", s.requireAuth(http.HandlerFunc(s.handleStatus)))

	// Protected control endpoints (3A-3): POST mutations that reflect into the
	// shared store; the daemon honors them on its next tick (no direct command).
	// Go 1.22 method-aware routing keeps these distinct from the GET patterns above.
	mux.Handle("POST /projects", s.requireAuth(http.HandlerFunc(s.handleOnboard)))
	mux.Handle("POST /projects/{id}/intake", s.requireAuth(http.HandlerFunc(s.handleIntake)))
	// Drafting helper (3B-4a): conversation → PROPOSED scenarios + intake-ready YAML
	// for human review. It persists NOTHING; approval flows through POST /intake.
	mux.Handle("POST /projects/{id}/distill", s.requireAuth(http.HandlerFunc(s.handleDistill)))
	mux.Handle("POST /projects/{id}/pause", s.requireAuth(http.HandlerFunc(s.handlePause)))
	mux.Handle("POST /projects/{id}/resume", s.requireAuth(http.HandlerFunc(s.handleResume)))
	mux.Handle("POST /projects/{id}/abort", s.requireAuth(http.HandlerFunc(s.handleAbort)))
	mux.Handle("POST /projects/{id}/approve", s.requireAuth(http.HandlerFunc(s.handleApprove)))

	// Historical event replay: standard bearer auth (header only).
	mux.Handle("GET /events", s.requireAuth(http.HandlerFunc(s.handleEvents)))

	// Live event push (WebSocket). NOT wrapped by requireAuth: it does its own
	// header-OR-?token= check (browsers cannot set an Authorization header on the
	// WebSocket handshake) and must reject BEFORE the upgrade — see handleWS.
	mux.HandleFunc("GET /ws", s.handleWS)

	return mux
}

// requireAuth enforces "Authorization: Bearer <token>" with a constant-time
// comparison against the configured token. Missing/malformed/wrong → 401 with a
// short JSON body; the expected token is NEVER revealed.
func (s *apiServer) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		h := r.Header.Get("Authorization")
		if len(h) <= len(prefix) || h[:len(prefix)] != prefix {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		got := h[len(prefix):]
		// Constant-time compare; the length-difference short-circuit in
		// subtle.ConstantTimeCompare does not leak the token value.
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- JSON DTOs (lowercase snake-ish tags, mirroring conductorctl/httpserver) ---

type projectDTO struct {
	ID               string `json:"id"`
	Repo             string `json:"repo"`
	BaseBranch       string `json:"base_branch"`
	HostID           string `json:"host_id"`
	Readiness        string `json:"readiness"`
	RecipePointer    string `json:"recipe_pointer"`
	GovernancePolicy string `json:"governance_policy"`
	Paused           bool   `json:"paused"`
}

type taskDTO struct {
	ID             string   `json:"id"`
	ProjectID      string   `json:"project_id"`
	Lane           string   `json:"lane"`
	Tier           string   `json:"tier"`
	Status         string   `json:"status"`
	Requires       []string `json:"requires"`
	Deps           []string `json:"deps"`
	Branch         string   `json:"branch"`
	ScenarioID     string   `json:"scenario_id"`
	RetryCount     int      `json:"retry_count"`
	AbortRequested bool     `json:"abort_requested"`
	Approved       bool     `json:"approved"`
}

type hostDTO struct {
	ID                  string   `json:"id"`
	Capabilities        []string `json:"capabilities"`
	LastHeartbeat       string   `json:"last_heartbeat,omitempty"`
	HeartbeatAgeSeconds int64    `json:"heartbeat_age_seconds"`
}

type leaseDTO struct {
	ProjectID  string `json:"project_id"`
	HostID     string `json:"host_id"`
	TaskID     string `json:"task_id"`
	AcquiredAt string `json:"acquired_at"`
}

type statusDTO struct {
	Projects    int        `json:"projects"`
	Hosts       int        `json:"hosts"`
	Leases      []leaseDTO `json:"leases"`
	GeneratedAt string     `json:"generated_at"`
}

// --- Handlers ---

// handleProjects: GET /projects → JSON array of all projects.
func (s *apiServer) handleProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.store.ListProjects(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	out := make([]projectDTO, 0, len(projects))
	for _, p := range projects {
		out = append(out, projectDTO{
			ID:               p.ID,
			Repo:             p.Repo,
			BaseBranch:       p.BaseBranch,
			HostID:           p.HostID,
			Readiness:        p.Readiness,
			RecipePointer:    p.RecipePointer,
			GovernancePolicy: p.GovernancePolicy,
			Paused:           p.Paused,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleProjectTasks: GET /projects/{id}/tasks → JSON array of that project's
// tasks, sorted by task ID. Unknown project → 404.
func (s *apiServer) handleProjectTasks(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if _, err := s.store.GetProject(r.Context(), id); err != nil {
		if errors.Is(err, statestore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	tasks, err := s.store.ListTasks(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Stable ordering by task id regardless of store insertion order.
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })

	out := make([]taskDTO, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, taskDTO{
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
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleHosts: GET /hosts → JSON array of registered hosts, sorted by host ID,
// each with capabilities and a clock-derived heartbeat age.
func (s *apiServer) handleHosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.store.ListHosts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].ID < hosts[j].ID })

	now := s.now()
	out := make([]hostDTO, 0, len(hosts))
	for _, h := range hosts {
		dto := hostDTO{
			ID:           h.ID,
			Capabilities: nonNil(h.Capabilities),
		}
		if !h.LastHeartbeat.IsZero() {
			dto.LastHeartbeat = h.LastHeartbeat.UTC().Format(time.RFC3339)
			age := int64(now.Sub(h.LastHeartbeat).Seconds())
			if age < 0 {
				age = 0
			}
			dto.HeartbeatAgeSeconds = age
		}
		out = append(out, dto)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleStatus: GET /status → fleet summary aggregate (project/host counts +
// active leases + generated_at). Distinct from the daemon's per-tick /status.
func (s *apiServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	projects, err := s.store.ListProjects(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	hosts, err := s.store.ListHosts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	leases, err := s.store.ListLeases(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	sort.Slice(leases, func(i, j int) bool { return leases[i].ProjectID < leases[j].ProjectID })

	leaseDTOs := make([]leaseDTO, 0, len(leases))
	for _, l := range leases {
		leaseDTOs = append(leaseDTOs, leaseDTO{
			ProjectID:  l.ProjectID,
			HostID:     l.HostID,
			TaskID:     l.TaskID,
			AcquiredAt: l.AcquiredAt.UTC().Format(time.RFC3339),
		})
	}

	writeJSON(w, http.StatusOK, statusDTO{
		Projects:    len(projects),
		Hosts:       len(hosts),
		Leases:      leaseDTOs,
		GeneratedAt: s.now().UTC().Format(time.RFC3339),
	})
}

// handleHealthz is liveness: always 200 while the process is serving. It does
// NOT consult the store, so a DB blip never flaps liveness.
func (s *apiServer) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleReadyz is readiness: 200 when the store answers a cheap ListProjects
// within the timeout, else 503 with a short reason (never a DSN). Mirrors the
// daemon's httpserver.go readiness philosophy.
func (s *apiServer) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
	defer cancel()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if _, err := s.store.ListProjects(ctx); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("not ready: store unreachable"))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready"))
}

// --- helpers ---

// nonNil returns s, or an empty non-nil slice when s is nil, so JSON encodes "[]"
// rather than "null" for empty collections.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// writeJSON encodes v as application/json with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError emits a short JSON error body. The message is a fixed, non-secret
// string — it NEVER includes the token, DSN, or a wrapped store error.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
