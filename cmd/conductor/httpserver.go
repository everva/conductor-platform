// Optional health/observability HTTP server for the conductor daemon (P4-2).
//
// This is the k8s-style probe surface: a small http.Server that runs ALONGSIDE
// the tick loop when -http-addr / CONDUCTOR_HTTP_ADDR is set, and is DISABLED by
// default (empty addr) so the daemon's existing behavior is unchanged. It exposes:
//
//   - GET /healthz  liveness  — 200 while the process/loop is alive. It does NOT
//     touch the store, so a transient DB blip never flaps liveness (which would
//     cause k8s to kill an otherwise-healthy daemon).
//   - GET /readyz   readiness — 200 when the StateStore is reachable (a cheap
//     read with a short timeout), 503 + a short reason otherwise. This is what
//     gates traffic/scheduling; it is allowed to flap on a DB blip.
//   - GET /status   ops JSON  — daemon project, last tick outcome/time/count,
//     uptime, store backend, governance on/off. It NEVER includes the DSN/password.
//
// Readiness checks the store WITHOUT touching the frozen statestore.StateStore
// interface: it issues a ListProjects (an existing read on the frozen contract)
// under a short timeout. A backend-reachability failure (pool down, timeout)
// surfaces as the read error, which is exactly the readiness signal we want; a
// healthy in-memory store returns immediately. No method is added to the frozen
// interface, and no concrete type is special-cased.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/everva/conductor-platform/internal/statestore"
)

// readinessTimeout bounds the store read the /readyz probe performs so a hung
// backend turns into a fast 503 rather than blocking the probe (and the k8s
// readiness gate) indefinitely.
const readinessTimeout = 2 * time.Second

// readyChecker is the seam /readyz probes for store reachability. The real daemon
// supplies a checker backed by statestore.StateStore.ListProjects; tests inject a
// fake whose check fails, so the 503 path is exercised offline with no database.
type readyChecker interface {
	// checkReady returns nil when the backing store is reachable, or an error
	// describing why it is not. It is given a context already bounded by a short
	// timeout.
	checkReady(ctx context.Context) error
}

// storeReadyChecker checks reachability via a cheap existing read on the frozen
// StateStore contract (ListProjects). It works for ANY backend — the in-memory
// store answers instantly, a Postgres store round-trips its pool — without adding
// to or type-asserting the frozen interface.
type storeReadyChecker struct {
	store statestore.StateStore
}

func (c storeReadyChecker) checkReady(ctx context.Context) error {
	_, err := c.store.ListProjects(ctx)
	return err
}

// tickSnapshot is a concurrency-safe record of the most recent tick, updated by
// the loop and read by /status. The loop owns the writes; the HTTP handlers only
// read, so a single mutex over a tiny struct keeps /status race-free without
// reaching into the running loop.
type tickSnapshot struct {
	mu      sync.RWMutex
	count   uint64
	outcome string
	at      time.Time
}

// record stores the latest tick's count, outcome and time. It is called from the
// tick path each tick; the lock is held only for the field copy.
func (s *tickSnapshot) record(count uint64, outcome string, at time.Time) {
	s.mu.Lock()
	s.count = count
	s.outcome = outcome
	s.at = at
	s.mu.Unlock()
}

// read returns a consistent copy of the latest tick snapshot for /status.
func (s *tickSnapshot) read() (count uint64, outcome string, at time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.count, s.outcome, s.at
}

// statusResponse is the /status JSON shape. It is intentionally ops-oriented and
// secret-free: there is NO dsn/password field — only the backend NAME
// (memory/postgres) is exposed, never the connection string.
type statusResponse struct {
	Project       string `json:"project"`
	StoreBackend  string `json:"store_backend"`
	Governance    bool   `json:"governance"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	Tick          uint64 `json:"tick"`
	LastOutcome   string `json:"last_outcome,omitempty"`
	LastTickAt    string `json:"last_tick_at,omitempty"`
}

// healthServer holds the dependencies the probe handlers need. It is built from
// the daemon's wiring (project/backend/governance + the live tick snapshot + a
// readiness checker) and exposes an http.Handler via routes().
type healthServer struct {
	project      string
	storeBackend string
	governance   bool
	startedAt    time.Time
	clock        func() time.Time
	snap         *tickSnapshot
	ready        readyChecker
}

// routes builds the probe mux. It is separated from the server lifecycle so tests
// can httptest the handlers directly without binding a socket.
func (h *healthServer) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", h.handleHealthz)
	mux.HandleFunc("/readyz", h.handleReadyz)
	mux.HandleFunc("/status", h.handleStatus)
	// /statusz alias so either spelling works for ops tooling.
	mux.HandleFunc("/statusz", h.handleStatus)
	return mux
}

// handleHealthz is liveness: always 200 while the process is serving. It does not
// consult the store, so a DB outage never makes the daemon look dead.
func (h *healthServer) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleReadyz is readiness: 200 when the store answers a cheap read within the
// timeout, else 503 with a short reason. This is the traffic/scheduling gate, so
// it is allowed to flap with backend health (unlike liveness).
func (h *healthServer) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
	defer cancel()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if err := h.ready.checkReady(ctx); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		// Short reason only; never echo a DSN or wrapped secret. The store read
		// errors here do not carry the password (the DSN is not part of the query).
		_, _ = w.Write([]byte("not ready: store unreachable"))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ready"))
}

// handleStatus emits the ops JSON snapshot. It reads the concurrency-safe
// tickSnapshot (never the running loop) and the static daemon identity. It omits
// the DSN entirely.
func (h *healthServer) handleStatus(w http.ResponseWriter, _ *http.Request) {
	count, outcome, at := h.snap.read()

	resp := statusResponse{
		Project:       h.project,
		StoreBackend:  h.storeBackend,
		Governance:    h.governance,
		UptimeSeconds: int64(h.clock().Sub(h.startedAt).Seconds()),
		Tick:          count,
		LastOutcome:   outcome,
	}
	if !at.IsZero() {
		resp.LastTickAt = at.UTC().Format(time.RFC3339)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	enc := json.NewEncoder(w)
	_ = enc.Encode(resp)
}

// errStoreUnreachable is a sentinel a fake readyChecker can return in tests to
// drive the 503 path deterministically; the real checker returns the store's own
// error.
var errStoreUnreachable = errors.New("store unreachable")
