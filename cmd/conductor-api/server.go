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
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/everva/conductor-platform/internal/credstore"
	"github.com/everva/conductor-platform/internal/events"
	"github.com/everva/conductor-platform/internal/intake"
	"github.com/everva/conductor-platform/internal/statestore"
	"github.com/everva/conductor-platform/internal/verify"
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
	// logger records the REAL cause of a 500 server-side (operators need it to
	// diagnose; the client only ever sees the fixed "internal error" string). It is
	// optional: a nil logger falls back to slog.Default(), so a zero-value apiServer
	// (and the many test constructors) still work. Wired to the process logger in main.
	logger *slog.Logger
	// distiller is the assisted-distillation seam (ADR-0005, ADR-0012) backing
	// POST /projects/{id}/distill: it turns a free-text conversation into PROPOSED,
	// shape-validated scenarios for human review (never persists). main.go wires the
	// production intake.NewCommandDistiller (claude -p); tests inject a stub. It is
	// drafting-only — when nil the distill endpoint returns 501 rather than panicking.
	distiller intake.Distiller
	// clock is the injectable time source for heartbeat-age and generated_at, so
	// time-derived JSON is deterministic in tests. Defaults to time.Now in main.
	clock func() time.Time
	// baseCtx is the server-lifetime context (cancelled on SIGINT/SIGTERM). The /ws
	// handler ties each live connection to it so a graceful shutdown closes streaming
	// clients promptly (review F3) instead of dropping them when the process exits.
	// It is optional: nil (e.g. in tests) leaves /ws bound to the request context only.
	baseCtx context.Context
	// sealer encrypts/decrypts the L3 credential store (ADR-0049): the editor uploads the
	// portable claude OAuth token, the gateway SEALS it before it touches Postgres, and the
	// agent fetches the decrypted token over the authed channel. It is OPTIONAL — nil when no
	// master key (CONDUCTOR_CREDENTIAL_KEY) is configured, in which case the credential
	// endpoints fail CLOSED (503) rather than ever storing/serving plaintext.
	sealer *credstore.Sealer
	// holdouts is the OPTIONAL holdout read+write seam (Faz-S): PUT /holdouts/{id} stores an
	// intake-approved holdout body, GET /agent/holdout serves it to the agent's verify gate. main
	// wires a *holdout.PGStore from the DSN (the central pg:// store); nil when no DSN → both
	// endpoints return 501. The holdout BODY is never logged (ADR-0018); it is repo-external by
	// construction (the central Postgres).
	holdouts holdoutStore
}

// holdoutStore is the narrow read+write seam the holdout endpoints drive (Faz-S). It mirrors the
// concrete *holdout.PGStore (Store from S1 + the frozen Fetch); declared here so the gateway tests
// inject a fake without a pgxpool. Store persists a body and returns its pg:// locator; Fetch
// resolves a locator to the injectable files.
type holdoutStore interface {
	Store(ctx context.Context, id string, files map[string][]byte) (string, error)
	Fetch(ctx context.Context, ref string) (verify.Holdout, error)
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
	mux.Handle("GET /projects/{id}/tasks/{task}/diff", s.requireAuth(http.HandlerFunc(s.handleProjectTaskDiff)))
	mux.Handle("GET /projects/{id}/scenarios", s.requireAuth(http.HandlerFunc(s.handleProjectScenarios)))
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
	mux.Handle("POST /projects/{id}/distill/stream", s.requireAuth(http.HandlerFunc(s.handleDistillStream)))
	mux.Handle("POST /projects/{id}/pause", s.requireAuth(http.HandlerFunc(s.handlePause)))
	mux.Handle("POST /projects/{id}/resume", s.requireAuth(http.HandlerFunc(s.handleResume)))
	mux.Handle("POST /projects/{id}/abort", s.requireAuth(http.HandlerFunc(s.handleAbort)))
	mux.Handle("POST /projects/{id}/approve", s.requireAuth(http.HandlerFunc(s.handleApprove)))
	mux.Handle("POST /projects/{id}/tasks/{task}/retry", s.requireAuth(http.HandlerFunc(s.handleRetry)))

	// Gateway-mediated host-agent API (ADR-0048, Faz G): a performer host leases work
	// and (Faz G2) reports results over HTTP, NEVER touching Postgres — the gateway
	// owns the store. Reuses the same registry/statestore seams as the in-process daemon.
	mux.Handle("POST /projects/{id}/agent/lease", s.requireAuth(http.HandlerFunc(s.handleAgentLease)))
	mux.Handle("POST /projects/{id}/agent/lease/release", s.requireAuth(http.HandlerFunc(s.handleAgentReleaseLease)))
	mux.Handle("POST /agent/heartbeat", s.requireAuth(http.HandlerFunc(s.handleAgentHeartbeat)))
	mux.Handle("POST /projects/{id}/agent/tasks/{task}/report", s.requireAuth(http.HandlerFunc(s.handleAgentReport)))
	mux.Handle("POST /projects/{id}/agent/tasks/{task}/result", s.requireAuth(http.HandlerFunc(s.handleAgentResult)))
	mux.Handle("POST /projects/{id}/agent/tasks/{task}/diff", s.requireAuth(http.HandlerFunc(s.handleAgentTaskDiff)))
	mux.Handle("GET /projects/{id}/agent/tasks/{task}/decision", s.requireAuth(http.HandlerFunc(s.handleAgentDecision)))
	mux.Handle("POST /projects/{id}/agent/tasks/{task}/merged", s.requireAuth(http.HandlerFunc(s.handleAgentMerged)))
	// Faz-S holdout store: the editor PUTs an intake-approved holdout body (S5), the agent's verify
	// gate GETs it (S3). Authed; the body is repo-external (central Postgres) and never logged.
	mux.Handle("PUT /holdouts/{id}", s.requireAuth(http.HandlerFunc(s.handlePutHoldout)))
	mux.Handle("GET /agent/holdout", s.requireAuth(http.HandlerFunc(s.handleGetHoldout)))

	// L3 (ADR-0049): the gateway-distributed encrypted credential store. The editor uploads the
	// director's portable claude OAuth token once (PUT), each agent fetches the decrypted token
	// at startup (GET), and logout removes it (DELETE). Authed; the token is sealed at rest and
	// only ever crosses the wire over this authed channel — never logged.
	mux.Handle("PUT /agent/credentials/{kind}", s.requireAuth(http.HandlerFunc(s.handleAgentPutCredential)))
	mux.Handle("GET /agent/credentials/{kind}", s.requireAuth(http.HandlerFunc(s.handleAgentGetCredential)))
	mux.Handle("DELETE /agent/credentials/{kind}", s.requireAuth(http.HandlerFunc(s.handleAgentDeleteCredential)))

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

type taskDiffDTO struct {
	ProjectID string `json:"project_id"`
	TaskID    string `json:"task_id"`
	Base      string `json:"base"`
	Branch    string `json:"branch"`
	Patch     string `json:"patch"`
	Truncated bool   `json:"truncated"`
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
		s.serverError(w, "projects: list", err)
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
		s.serverError(w, "tasks: get project", err)
		return
	}

	tasks, err := s.store.ListTasks(r.Context(), id)
	if err != nil {
		s.serverError(w, "tasks: list", err)
		return
	}
	// Stable ordering by task id regardless of store insertion order.
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })

	out := make([]taskDTO, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, toTaskDTO(t))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleProjectTaskDiff: GET /projects/{id}/tasks/{task}/diff → the persisted FULL-context
// diff (P2b, ADR-0041) for the editor's full-file native vscode.diff. 404 when none is stored
// (the editor falls back to the bounded KindDiff event patch), 501 when the configured store has
// no diff persistence (a non-PG/non-memory store). Additive read; the gateway stays a pure
// projection of the store and never sees the worktree (the worker persists at the gate).
func (s *apiServer) handleProjectTaskDiff(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	taskID := r.PathValue("task")

	tds, ok := s.store.(statestore.TaskDiffStore)
	if !ok {
		writeError(w, http.StatusNotImplemented, "diff storage not configured")
		return
	}
	d, err := tds.GetTaskDiff(r.Context(), id, taskID)
	if errors.Is(err, statestore.ErrNotFound) {
		writeError(w, http.StatusNotFound, "diff not found")
		return
	}
	if err != nil {
		s.serverError(w, "task diff: get", err)
		return
	}
	writeJSON(w, http.StatusOK, taskDiffDTO{
		ProjectID: d.ProjectID,
		TaskID:    d.TaskID,
		Base:      d.Base,
		Branch:    d.Branch,
		Patch:     d.Patch,
		Truncated: d.Truncated,
	})
}

// handleProjectScenarios: GET /projects/{id}/scenarios → JSON array of that
// project's scenarios (id/title/lane/tier/deps/acceptance + the holdout ref),
// sorted by scenario ID. The agent-native session view (redesign E2) reads a
// task's SPEC — its acceptance criteria — here via the task's scenario_id, so a
// director sees what the agent is being held to. Unknown project → 404. Additive
// read (B2); the gateway stays a pure projection of the frozen StateStore (ADR-0025).
func (s *apiServer) handleProjectScenarios(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	if _, err := s.store.GetProject(r.Context(), id); err != nil {
		if errors.Is(err, statestore.ErrNotFound) {
			writeError(w, http.StatusNotFound, "project not found")
			return
		}
		s.serverError(w, "scenarios: get project", err)
		return
	}

	scenarios, err := s.store.ListScenarios(r.Context(), id)
	if err != nil {
		s.serverError(w, "scenarios: list", err)
		return
	}
	sort.Slice(scenarios, func(i, j int) bool { return scenarios[i].ID < scenarios[j].ID })

	out := make([]scenarioDTO, 0, len(scenarios))
	for _, sc := range scenarios {
		out = append(out, scenarioDTO{
			ID:         sc.ID,
			Title:      sc.Title,
			Lane:       sc.Lane,
			Tier:       sc.Tier,
			Deps:       nonNil(sc.Deps),
			Acceptance: nonNil(sc.Acceptance),
			HoldoutRef: sc.HoldoutRef,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleHosts: GET /hosts → JSON array of registered hosts, sorted by host ID,
// each with capabilities and a clock-derived heartbeat age.
func (s *apiServer) handleHosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.store.ListHosts(r.Context())
	if err != nil {
		s.serverError(w, "hosts: list", err)
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
		s.serverError(w, "status: list projects", err)
		return
	}
	hosts, err := s.store.ListHosts(r.Context())
	if err != nil {
		s.serverError(w, "status: list hosts", err)
		return
	}
	leases, err := s.store.ListLeases(r.Context())
	if err != nil {
		s.serverError(w, "status: list leases", err)
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

// serverError logs the REAL cause of a 500 server-side and returns the fixed,
// secret-free "internal error" body to the client. Before this existed every 500
// swallowed its cause, so an operator saw "internal error" with no signal — the
// gateway-onboard-500 bug (a gateway pointed at an unmigrated DB) was invisible in
// the logs. op is a short STATIC label (e.g. "onboard: list projects"), never
// request data. Logging err is safe: statestore/pgx *query* errors carry the SQL
// state and message but NOT the DSN/password (credentials surface only at
// connection time, which happens at startup — never inside a handler).
func (s *apiServer) serverError(w http.ResponseWriter, op string, err error) {
	lg := s.logger
	if lg == nil {
		lg = slog.Default()
	}
	lg.Error("conductor-api handler error", slog.String("op", op), slog.Any("err", err))
	writeError(w, http.StatusInternalServerError, "internal error")
}
