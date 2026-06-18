// Health/observability HTTP server tests (P4-2): hermetic, no network, no DB.
// They httptest the probe handlers directly (liveness always-200, readiness
// 200/503 over an injected store-reachability check, status JSON shape with NO
// dsn field), plus one end-to-end test that binds an ephemeral :0 port, hits
// /healthz + /readyz over real HTTP, and asserts a clean shutdown with no leak.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// okChecker is a readyChecker that always reports the store reachable.
type okChecker struct{}

func (okChecker) checkReady(context.Context) error { return nil }

// failChecker is a readyChecker that always reports the store unreachable, driving
// the 503 path deterministically with no real (failing) database.
type failChecker struct{}

func (failChecker) checkReady(context.Context) error { return errStoreUnreachable }

// newTestHealthServer builds a healthServer with a fixed clock and the given
// readiness checker, seeded with a last-tick snapshot so /status has data.
func newTestHealthServer(ready readyChecker) *healthServer {
	started := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	now := started.Add(42 * time.Second)
	snap := &tickSnapshot{}
	snap.record(7, "noop", now)
	return &healthServer{
		project:      "test-proj",
		storeBackend: "memory",
		governance:   true,
		startedAt:    started,
		clock:        func() time.Time { return now },
		snap:         snap,
		ready:        ready,
	}
}

// TestHealthz_Always200 proves liveness is 200 regardless of store health: even
// with a failing readiness checker, /healthz stays 200 (liveness must not flap on
// a DB blip).
func TestHealthz_Always200(t *testing.T) {
	for _, ready := range []readyChecker{okChecker{}, failChecker{}} {
		h := newTestHealthServer(ready)
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rec := httptest.NewRecorder()
		h.routes().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("/healthz code = %d, want 200 (liveness independent of store)", rec.Code)
		}
		if got := strings.TrimSpace(rec.Body.String()); got != "ok" {
			t.Fatalf("/healthz body = %q, want ok", got)
		}
	}
}

// TestReadyz_Healthy200 proves readiness is 200 when the store check succeeds.
func TestReadyz_Healthy200(t *testing.T) {
	h := newTestHealthServer(okChecker{})
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("/readyz code = %d, want 200 for a reachable store", rec.Code)
	}
}

// TestReadyz_Unhealthy503 proves readiness is 503 (with a short reason) when the
// store check fails — the traffic/scheduling gate closes on backend trouble.
func TestReadyz_Unhealthy503(t *testing.T) {
	h := newTestHealthServer(failChecker{})
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz code = %d, want 503 for an unreachable store", rec.Code)
	}
	if rec.Body.Len() == 0 {
		t.Fatal("/readyz 503 body is empty, want a short reason")
	}
}

// TestStatus_JSONShape proves /status emits the expected ops fields and, crucially,
// NEVER a dsn/password field. It checks both the decoded struct and the raw bytes
// (so an accidental dsn key in any form is caught).
func TestStatus_JSONShape(t *testing.T) {
	h := newTestHealthServer(okChecker{})

	for _, path := range []string{"/status", "/statusz"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.routes().ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s code = %d, want 200", path, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Fatalf("%s Content-Type = %q, want application/json", path, ct)
		}

		var resp statusResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("%s body not valid JSON: %v", path, err)
		}
		if resp.Project != "test-proj" {
			t.Fatalf("%s project = %q, want test-proj", path, resp.Project)
		}
		if resp.StoreBackend != "memory" {
			t.Fatalf("%s store_backend = %q, want memory", path, resp.StoreBackend)
		}
		if !resp.Governance {
			t.Fatalf("%s governance = false, want true", path)
		}
		if resp.Tick != 7 {
			t.Fatalf("%s tick = %d, want 7", path, resp.Tick)
		}
		if resp.LastOutcome != "noop" {
			t.Fatalf("%s last_outcome = %q, want noop", path, resp.LastOutcome)
		}
		if resp.UptimeSeconds != 42 {
			t.Fatalf("%s uptime_seconds = %d, want 42", path, resp.UptimeSeconds)
		}
		if resp.LastTickAt == "" {
			t.Fatalf("%s last_tick_at is empty, want a timestamp", path)
		}

		// HARD constraint: the status payload must never expose the DSN/password.
		raw := bytes.ToLower(rec.Body.Bytes())
		for _, banned := range []string{"dsn", "password", "postgres://", "passwd", "secret"} {
			if bytes.Contains(raw, []byte(banned)) {
				t.Fatalf("%s body contains banned token %q (secret leak): %s", path, banned, rec.Body.String())
			}
		}
	}
}

// TestStatus_NoLastTick omits last-tick fields before the first tick (the
// snapshot is zero), so a freshly started daemon's /status is still valid JSON
// without a bogus zero timestamp.
func TestStatus_NoLastTick(t *testing.T) {
	started := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	h := &healthServer{
		project:      "p",
		storeBackend: "memory",
		startedAt:    started,
		clock:        func() time.Time { return started.Add(time.Second) },
		snap:         &tickSnapshot{}, // never recorded
		ready:        okChecker{},
	}
	req := httptest.NewRequest(http.MethodGet, "/status", nil)
	rec := httptest.NewRecorder()
	h.routes().ServeHTTP(rec, req)

	var resp statusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("status body not valid JSON: %v", err)
	}
	if resp.Tick != 0 {
		t.Fatalf("tick = %d, want 0 before first tick", resp.Tick)
	}
	if resp.LastTickAt != "" {
		t.Fatalf("last_tick_at = %q, want empty before first tick", resp.LastTickAt)
	}
}

// TestHealthServer_RealHTTP_StartStop is the high-confidence end-to-end test: it
// builds a loop-mode in-memory daemon with -http-addr=:0, runs it, hits /healthz
// and /readyz over REAL HTTP on the ephemeral port, then cancels the context and
// asserts the daemon (and its health server) shuts down cleanly within the
// deadline — no goroutine/socket leak. Fully hermetic (no DB).
func TestHealthServer_RealHTTP_StartStop(t *testing.T) {
	cfg := testConfig(t)
	cfg.once = false
	cfg.httpAddr = "127.0.0.1:0" // ephemeral port
	cfg.governance = true

	d, err := newDaemon(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("newDaemon: %v", err)
	}
	defer d.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	// Discover the bound port by polling the health server's listener address.
	// startHealthServer binds synchronously inside Run, so a short poll is enough.
	addr := waitForHealthAddr(t, d)

	base := "http://" + addr
	assertGet(t, base+"/healthz", http.StatusOK, "ok")
	assertGet(t, base+"/readyz", http.StatusOK, "") // in-memory store is reachable

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error on shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return within 3s of cancel (health server leak?)")
	}

	// After shutdown the port must be closed: a fresh request fails to connect.
	client := &http.Client{Timeout: 500 * time.Millisecond}
	if resp, err := client.Get(base + "/healthz"); err == nil {
		_ = resp.Body.Close()
		t.Fatal("health server still serving after shutdown; expected the listener closed")
	}
}

// waitForHealthAddr polls until the daemon's health server has bound its listener
// and recorded its real address (boundHealthAddr), resolving the ephemeral ":0"
// port. startHealthServer binds synchronously inside Run, so a short poll suffices.
func waitForHealthAddr(t *testing.T, d *Daemon) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if a := d.boundHealthAddr(); a != "" {
			return a
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("health server did not bind a listener within 2s")
	return ""
}

// assertGet performs a GET and asserts the status code and (when nonempty) the
// trimmed body.
func assertGet(t *testing.T, url string, wantCode int, wantBody string) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != wantCode {
		t.Fatalf("GET %s code = %d, want %d", url, resp.StatusCode, wantCode)
	}
	if wantBody != "" {
		b, _ := io.ReadAll(resp.Body)
		if got := strings.TrimSpace(string(b)); got != wantBody {
			t.Fatalf("GET %s body = %q, want %q", url, got, wantBody)
		}
	}
}
