// Package main daemon tests: hermetic, no network, no real `claude -p`. They
// prove the daemon's loop policy directly — a single -once tick over an in-memory
// setup with no ready tasks no-ops cleanly (the path tests/CI exercise), and a
// loop returns promptly when its context is cancelled (graceful shutdown). The
// no-op tick never touches git/provisioner/engine, so these run fast and offline.
package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/everva/conductor-platform/internal/events"
	"github.com/everva/conductor-platform/internal/heartbeat"
	"github.com/everva/conductor-platform/internal/statestore"
)

// newTestLogger returns a logger that discards output so test logs stay clean.
func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

// testConfig is a minimal valid config for an in-memory, hermetic daemon: a fresh
// store with the seeded project but NO ready task, so every tick is a clean no-op.
func testConfig(t *testing.T) config {
	t.Helper()
	return config{
		project:    "test-proj",
		rootDir:    t.TempDir(),
		baseBranch: "develop",
		interval:   10 * time.Millisecond,
		timeout:    time.Second,
		developCmd: []string{"true"}, // never invoked: no ready task to develop.
		hostID:     "test-host",
	}
}

// TestRunOnce_NoReadyTask_NoOpsCleanly is the CI path: -once over a store with no
// ready task must run exactly one tick that no-ops and return nil (exit 0). The
// no-op path takes no lease and touches no git, so it is fully hermetic.
func TestRunOnce_NoReadyTask_NoOpsCleanly(t *testing.T) {
	cfg := testConfig(t)
	cfg.once = true

	d, err := newDaemon(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("newDaemon: %v", err)
	}

	res, err := d.tick(context.Background())
	if err != nil {
		t.Fatalf("once tick returned error: %v", err)
	}
	if res.Outcome != "noop" {
		t.Fatalf("once tick outcome = %q, want noop (no ready task)", res.Outcome)
	}

	// Run() in once mode must also return nil and run a single tick.
	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("Run(once) returned error: %v", err)
	}
}

// TestRun_GracefulShutdown asserts the loop returns cleanly (nil error) when its
// context is cancelled, rather than spinning forever. We cancel almost
// immediately; the loop must observe ctx.Done and unwind.
func TestRun_GracefulShutdown(t *testing.T) {
	cfg := testConfig(t)
	cfg.once = false

	d, err := newDaemon(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("newDaemon: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	// Let the immediate first tick run, then signal shutdown.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error on graceful shutdown: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of context cancellation (no graceful shutdown)")
	}
}

// TestRun_ContextAlreadyCancelled proves a loop started with an already-cancelled
// context returns promptly without hanging (defensive: shutdown raced startup).
func TestRun_ContextAlreadyCancelled(t *testing.T) {
	cfg := testConfig(t)
	cfg.once = false

	d, err := newDaemon(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("newDaemon: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return for an already-cancelled context")
	}
}

// TestParseConfig validates flag parsing, env fallback, and the required-field /
// Faz-1a constraint checks.
func TestParseConfig(t *testing.T) {
	t.Run("happy path with flags", func(t *testing.T) {
		// Isolate the default-asserting field (baseBranch) from ambient daemon env.
		t.Setenv("CONDUCTOR_BASE_BRANCH", "")
		cfg, err := parseConfig([]string{
			"-project", "p1", "-root", "/tmp/r", "-once",
			"-develop-cmd", "claude -p --foo",
		}, io.Discard)
		if err != nil {
			t.Fatalf("parseConfig: %v", err)
		}
		if cfg.project != "p1" || cfg.rootDir != "/tmp/r" || !cfg.once {
			t.Fatalf("unexpected cfg: %+v", cfg)
		}
		if got, want := cfg.developCmd, []string{"claude", "-p", "--foo"}; !equalStrs(got, want) {
			t.Fatalf("developCmd = %v, want %v", got, want)
		}
		if cfg.baseBranch != "develop" {
			t.Fatalf("baseBranch = %q, want develop (default)", cfg.baseBranch)
		}
	})

	t.Run("missing project errors", func(t *testing.T) {
		if _, err := parseConfig([]string{"-root", "/tmp/r"}, io.Discard); err == nil {
			t.Fatal("expected error for missing -project")
		}
	})

	t.Run("missing root errors", func(t *testing.T) {
		if _, err := parseConfig([]string{"-project", "p1"}, io.Discard); err == nil {
			t.Fatal("expected error for missing -root")
		}
	})

	t.Run("max-concurrent must be 1", func(t *testing.T) {
		if _, err := parseConfig([]string{"-project", "p1", "-root", "/tmp/r", "-max-concurrent", "2"}, io.Discard); err == nil {
			t.Fatal("expected error for -max-concurrent 2")
		}
	})

	t.Run("dsn defaults empty (in-memory)", func(t *testing.T) {
		// Isolate from ambient daemon env: a CONDUCTOR_DSN in the caller's
		// environment (e.g. when the platform's own suite runs under a daemon-set
		// env, or under the sanitized verify gate) must not flip this default
		// assertion. t.Setenv("","") forces the empty-default branch deterministically.
		t.Setenv("CONDUCTOR_DSN", "")
		cfg, err := parseConfig([]string{"-project", "p1", "-root", "/tmp/r"}, io.Discard)
		if err != nil {
			t.Fatalf("parseConfig: %v", err)
		}
		if cfg.dsn != "" {
			t.Fatalf("dsn = %q, want empty by default (in-memory)", cfg.dsn)
		}
	})

	t.Run("dsn from flag", func(t *testing.T) {
		cfg, err := parseConfig([]string{
			"-project", "p1", "-root", "/tmp/r",
			"-dsn", "postgres://u:p@h:5432/db",
		}, io.Discard)
		if err != nil {
			t.Fatalf("parseConfig: %v", err)
		}
		if cfg.dsn != "postgres://u:p@h:5432/db" {
			t.Fatalf("dsn = %q, want the flag value", cfg.dsn)
		}
	})

	t.Run("dsn from env (CONDUCTOR_DSN)", func(t *testing.T) {
		t.Setenv("CONDUCTOR_DSN", "postgres://envhost/db")
		cfg, err := parseConfig([]string{"-project", "p1", "-root", "/tmp/r"}, io.Discard)
		if err != nil {
			t.Fatalf("parseConfig: %v", err)
		}
		if cfg.dsn != "postgres://envhost/db" {
			t.Fatalf("dsn = %q, want env value", cfg.dsn)
		}
	})

	t.Run("dsn flag overrides env", func(t *testing.T) {
		t.Setenv("CONDUCTOR_DSN", "postgres://envhost/db")
		cfg, err := parseConfig([]string{
			"-project", "p1", "-root", "/tmp/r", "-dsn", "postgres://flaghost/db",
		}, io.Discard)
		if err != nil {
			t.Fatalf("parseConfig: %v", err)
		}
		if cfg.dsn != "postgres://flaghost/db" {
			t.Fatalf("dsn = %q, want flag to win over env", cfg.dsn)
		}
	})

	t.Run("env fallback", func(t *testing.T) {
		t.Setenv("CONDUCTOR_PROJECT", "envproj")
		t.Setenv("CONDUCTOR_ROOT", "/tmp/envroot")
		cfg, err := parseConfig(nil, io.Discard)
		if err != nil {
			t.Fatalf("parseConfig: %v", err)
		}
		if cfg.project != "envproj" || cfg.rootDir != "/tmp/envroot" {
			t.Fatalf("env fallback failed: %+v", cfg)
		}
	})

	t.Run("flag overrides env", func(t *testing.T) {
		t.Setenv("CONDUCTOR_PROJECT", "envproj")
		cfg, err := parseConfig([]string{"-project", "flagproj", "-root", "/tmp/r"}, io.Discard)
		if err != nil {
			t.Fatalf("parseConfig: %v", err)
		}
		if cfg.project != "flagproj" {
			t.Fatalf("project = %q, want flagproj (flag should win over env)", cfg.project)
		}
	})
}

// TestParseConfig_HTTPAddr proves the -http-addr flag / CONDUCTOR_HTTP_ADDR env
// is parsed, defaults to empty (server DISABLED → unchanged behavior), and the
// flag wins over the env.
func TestParseConfig_HTTPAddr(t *testing.T) {
	t.Run("defaults empty (disabled)", func(t *testing.T) {
		t.Setenv("CONDUCTOR_HTTP_ADDR", "") // isolate from ambient daemon env.
		cfg, err := parseConfig([]string{"-project", "p1", "-root", "/tmp/r"}, io.Discard)
		if err != nil {
			t.Fatalf("parseConfig: %v", err)
		}
		if cfg.httpAddr != "" {
			t.Fatalf("httpAddr = %q, want empty by default (HTTP server disabled)", cfg.httpAddr)
		}
	})

	t.Run("from flag", func(t *testing.T) {
		cfg, err := parseConfig([]string{"-project", "p1", "-root", "/tmp/r", "-http-addr", ":8080"}, io.Discard)
		if err != nil {
			t.Fatalf("parseConfig: %v", err)
		}
		if cfg.httpAddr != ":8080" {
			t.Fatalf("httpAddr = %q, want :8080", cfg.httpAddr)
		}
	})

	t.Run("from env", func(t *testing.T) {
		t.Setenv("CONDUCTOR_HTTP_ADDR", ":9090")
		cfg, err := parseConfig([]string{"-project", "p1", "-root", "/tmp/r"}, io.Discard)
		if err != nil {
			t.Fatalf("parseConfig: %v", err)
		}
		if cfg.httpAddr != ":9090" {
			t.Fatalf("httpAddr = %q, want :9090 from env", cfg.httpAddr)
		}
	})

	t.Run("flag overrides env", func(t *testing.T) {
		t.Setenv("CONDUCTOR_HTTP_ADDR", ":9090")
		cfg, err := parseConfig([]string{"-project", "p1", "-root", "/tmp/r", "-http-addr", ":7070"}, io.Discard)
		if err != nil {
			t.Fatalf("parseConfig: %v", err)
		}
		if cfg.httpAddr != ":7070" {
			t.Fatalf("httpAddr = %q, want :7070 (flag wins over env)", cfg.httpAddr)
		}
	})
}

// TestDaemon_HTTPDisabledByDefault proves a daemon built with no -http-addr (the
// default) has NO health server, so the daemon behaves exactly as before — no
// server starts and startHealthServer is a no-op closure.
func TestDaemon_HTTPDisabledByDefault(t *testing.T) {
	cfg := testConfig(t) // httpAddr is the zero value (empty).
	d, err := newDaemon(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("newDaemon: %v", err)
	}
	defer d.Close()

	if d.health != nil {
		t.Fatal("health server is non-nil with empty http-addr; want disabled by default")
	}
	// startHealthServer must be a safe no-op when disabled.
	stop := d.startHealthServer(context.Background())
	stop()
	if d.boundHealthAddr() != "" {
		t.Fatalf("boundHealthAddr = %q, want empty when server disabled", d.boundHealthAddr())
	}
}

// TestParseConfig_Governance proves the -governance flag defaults to true (so
// production human-gates high-tier merges), is togglable to false, and honors the
// CONDUCTOR_GOVERNANCE env fallback with the flag winning over env.
func TestParseConfig_Governance(t *testing.T) {
	t.Run("defaults true", func(t *testing.T) {
		t.Setenv("CONDUCTOR_GOVERNANCE", "") // isolate from ambient daemon env.
		cfg, err := parseConfig([]string{"-project", "p1", "-root", "/tmp/r"}, io.Discard)
		if err != nil {
			t.Fatalf("parseConfig: %v", err)
		}
		if !cfg.governance {
			t.Fatal("governance = false, want true by default (production human-gates T3/T4)")
		}
	})

	t.Run("disable via flag", func(t *testing.T) {
		cfg, err := parseConfig([]string{"-project", "p1", "-root", "/tmp/r", "-governance=false"}, io.Discard)
		if err != nil {
			t.Fatalf("parseConfig: %v", err)
		}
		if cfg.governance {
			t.Fatal("governance = true, want false after -governance=false")
		}
	})

	t.Run("env fallback", func(t *testing.T) {
		t.Setenv("CONDUCTOR_GOVERNANCE", "false")
		cfg, err := parseConfig([]string{"-project", "p1", "-root", "/tmp/r"}, io.Discard)
		if err != nil {
			t.Fatalf("parseConfig: %v", err)
		}
		if cfg.governance {
			t.Fatal("governance = true, want false from CONDUCTOR_GOVERNANCE=false")
		}
	})

	t.Run("flag overrides env", func(t *testing.T) {
		t.Setenv("CONDUCTOR_GOVERNANCE", "false")
		cfg, err := parseConfig([]string{"-project", "p1", "-root", "/tmp/r", "-governance=true"}, io.Discard)
		if err != nil {
			t.Fatalf("parseConfig: %v", err)
		}
		if !cfg.governance {
			t.Fatal("governance = false, want true (flag should win over env)")
		}
	})
}

// TestNewPolicy_GovernanceToggle proves the governance on/off switch maps to a
// non-nil / nil conductor.Policy: ON wires the risk-layered policy (so T3/T4 are
// human-gated), OFF leaves it nil (auto-merge-all). The Deps.Policy field is
// populated from exactly this value.
func TestNewPolicy_GovernanceToggle(t *testing.T) {
	on := config{governance: true}
	if newPolicy(on, newTestLogger()) == nil {
		t.Fatal("newPolicy(governance=true) = nil, want a non-nil risk-layered policy")
	}

	off := config{governance: false}
	if p := newPolicy(off, newTestLogger()); p != nil {
		t.Fatalf("newPolicy(governance=false) = %v, want nil (auto-merge-all)", p)
	}
}

// TestNewEmitter_BackendByDSN proves the event Emitter backend follows -dsn,
// mirroring newStore: empty dsn selects the in-memory bus (observable in-process),
// and the returned closer is a non-nil, safe-to-call teardown. The Postgres branch
// (dsn set) is exercised by the optional real-DB test.
func TestNewEmitter_BackendByDSN(t *testing.T) {
	cfg := testConfig(t) // dsn is the zero value (empty) -> memory event bus.

	emitter, closer, err := newEmitter(context.Background(), cfg, newTestLogger())
	if err != nil {
		t.Fatalf("newEmitter(empty dsn): %v", err)
	}
	if emitter == nil {
		t.Fatal("emitter is nil; want the in-memory event bus for empty dsn")
	}
	if closer == nil {
		t.Fatal("closer is nil; want a non-nil teardown for the memory event bus")
	}
	if _, ok := emitter.(*events.MemoryBus); !ok {
		t.Fatalf("emitter type = %T, want *events.MemoryBus for empty dsn", emitter)
	}

	// The memory bus satisfies the conductor.Emitter seam directly: Publish is
	// callable and observable in-process, with no adapter.
	ev := events.Event{Project: "p", Phase: events.PhaseDevelop, Kind: events.KindStarted}
	if err := emitter.Publish(context.Background(), ev); err != nil {
		t.Fatalf("Publish on memory emitter: %v", err)
	}

	closer() // must not panic for the memory backend.
}

// TestRunExitCodes checks the top-level run() exit codes for the help and
// missing-required-flag paths without spawning a process.
func TestRunExitCodes(t *testing.T) {
	t.Run("help is exit 0", func(t *testing.T) {
		if code := run(context.Background(), []string{"-h"}, newTestLogger(), io.Discard); code != 0 {
			t.Fatalf("run(-h) = %d, want 0", code)
		}
	})
	t.Run("missing required flag is exit 2", func(t *testing.T) {
		if code := run(context.Background(), nil, newTestLogger(), io.Discard); code != 2 {
			t.Fatalf("run(nil) = %d, want 2", code)
		}
	})
	t.Run("once no-op is exit 0", func(t *testing.T) {
		code := run(context.Background(), []string{"-project", "p1", "-root", t.TempDir(), "-once"}, newTestLogger(), io.Discard)
		if code != 0 {
			t.Fatalf("run(-once no-op) = %d, want 0", code)
		}
	})
}

// TestParseConfig_CheckMode proves -check only needs -heartbeat (not
// project/root), and that an empty heartbeat path is rejected in check mode.
func TestParseConfig_CheckMode(t *testing.T) {
	t.Run("check needs only heartbeat", func(t *testing.T) {
		cfg, err := parseConfig([]string{"-check", "-heartbeat", "/tmp/hb.json"}, io.Discard)
		if err != nil {
			t.Fatalf("parseConfig: %v", err)
		}
		if !cfg.check || cfg.heartbeatPath != "/tmp/hb.json" {
			t.Fatalf("unexpected check cfg: %+v", cfg)
		}
		if cfg.heartbeatStale <= 0 {
			t.Fatalf("heartbeatStale should default positive, got %s", cfg.heartbeatStale)
		}
	})

	t.Run("check without heartbeat errors", func(t *testing.T) {
		if _, err := parseConfig([]string{"-check"}, io.Discard); err == nil {
			t.Fatal("expected error: -check requires -heartbeat")
		}
	})
}

// TestRunCheck_ExitCodes drives the independent stall-detector entry point and
// asserts its exit-code contract (FRESH=0, STALE=1, MISSING=2) over an injected
// heartbeat file. It tests the inner run path (runCheck), not os.Exit.
func TestRunCheck_ExitCodes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hb.json")
	at := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

	// MISSING: no file yet -> exit 2.
	missingCfg := config{check: true, heartbeatPath: path, heartbeatStale: time.Minute}
	if code := runCheck(missingCfg, newTestLogger(), io.Discard); code != 2 {
		t.Fatalf("runCheck(missing) = %d, want 2", code)
	}

	// Write a heartbeat stamped at `at`.
	w := heartbeat.NewWriter(path, "h", "p", 1, func() time.Time { return at })
	if err := w.Write(1, "noop"); err != nil {
		t.Fatalf("seed heartbeat: %v", err)
	}

	// FRESH: a generous threshold makes it fresh regardless of wall-clock skew at
	// test time (the heartbeat was just written, so age is small).
	freshCfg := config{check: true, heartbeatPath: path, heartbeatStale: time.Hour}
	if code := runCheck(freshCfg, newTestLogger(), io.Discard); code != 0 {
		t.Fatalf("runCheck(fresh) = %d, want 0", code)
	}

	// STALE: a tiny threshold against a fixed-in-the-past stamp. We can't inject
	// the clock through runCheck, so write a heartbeat far in the past instead.
	stalePath := filepath.Join(dir, "stale.json")
	oldW := heartbeat.NewWriter(stalePath, "h", "p", 1, func() time.Time { return time.Now().Add(-time.Hour) })
	if err := oldW.Write(1, "noop"); err != nil {
		t.Fatalf("seed stale heartbeat: %v", err)
	}
	staleCfg := config{check: true, heartbeatPath: stalePath, heartbeatStale: time.Minute}
	if code := runCheck(staleCfg, newTestLogger(), io.Discard); code != 1 {
		t.Fatalf("runCheck(stale) = %d, want 1", code)
	}
}

// TestDaemonWritesHeartbeat proves the tick loop writes a heartbeat when a path
// is configured, and that the record carries the advancing tick count + outcome.
func TestDaemonWritesHeartbeat(t *testing.T) {
	cfg := testConfig(t)
	cfg.once = true
	cfg.heartbeatPath = filepath.Join(t.TempDir(), "hb.json")

	d, err := newDaemon(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("newDaemon: %v", err)
	}
	if _, err := d.tick(context.Background()); err != nil {
		t.Fatalf("tick: %v", err)
	}

	rec, err := heartbeat.Read(cfg.heartbeatPath)
	if err != nil {
		t.Fatalf("Read heartbeat: %v", err)
	}
	if rec.Tick != 1 {
		t.Fatalf("heartbeat tick = %d, want 1 after one tick", rec.Tick)
	}
	if rec.LastOutcome != "noop" {
		t.Fatalf("heartbeat outcome = %q, want noop", rec.LastOutcome)
	}
	if rec.Project != cfg.project {
		t.Fatalf("heartbeat project = %q, want %q", rec.Project, cfg.project)
	}
}

// TestNewStore_EmptyDSNUsesMemory proves the default (empty dsn) path selects the
// in-memory store and returns a non-nil no-op closer that is safe to call. This
// is the existing dev/test behavior and must stay unchanged.
func TestNewStore_EmptyDSNUsesMemory(t *testing.T) {
	cfg := testConfig(t) // dsn is the zero value (empty) -> memory.

	store, closer, err := newStore(context.Background(), cfg, newTestLogger())
	if err != nil {
		t.Fatalf("newStore(empty dsn): %v", err)
	}
	if closer == nil {
		t.Fatal("closer is nil; want a non-nil no-op closer for the memory store")
	}
	if _, ok := store.(*statestore.MemoryStore); !ok {
		// Type-assert against the concrete memory store to confirm backend selection.
		t.Fatalf("store type = %T, want *statestore.MemoryStore for empty dsn", store)
	}
	closer() // must not panic for the memory backend.
}

// TestRealDB_OnceTickMigratesAndRuns is the OPTIONAL real-database test: when
// TEST_DATABASE_URL is set it constructs the Postgres-backed daemon (which
// migrates the schema on startup) and runs a single -once tick, asserting it
// no-ops cleanly over a fresh, un-onboarded project. It is SKIPPED when the env
// var is unset, so the default `go test ./...` stays green with no database.
func TestRealDB_OnceTickMigratesAndRuns(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL unset; skipping real-DB test")
	}

	cfg := testConfig(t)
	cfg.once = true
	cfg.dsn = dsn
	cfg.project = "p3-1-realdb-test"
	cfg.governance = true

	// With a DSN set, the event Emitter backend must be the Postgres LISTEN/NOTIFY
	// bus so external subscribers see events. (The store migrations create the
	// events table newDaemon's bus publishes to.) Construct + close it directly
	// first to assert backend selection, then run the full daemon below.
	if err := statestore.MigrateDSN(context.Background(), dsn); err != nil {
		t.Fatalf("MigrateDSN: %v", err)
	}
	emitter, busCloser, err := newEmitter(context.Background(), cfg, newTestLogger())
	if err != nil {
		t.Fatalf("newEmitter(postgres): %v", err)
	}
	if _, ok := emitter.(*events.PostgresBus); !ok {
		t.Fatalf("emitter type = %T, want *events.PostgresBus for a set dsn", emitter)
	}
	busCloser()

	d, err := newDaemon(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("newDaemon(postgres): %v", err)
	}
	defer d.Close()

	res, err := d.tick(context.Background())
	if err != nil {
		t.Fatalf("once tick over postgres returned error: %v", err)
	}
	if res.Outcome != "noop" {
		t.Fatalf("once tick outcome = %q, want noop (no ready task)", res.Outcome)
	}

	// Re-running newDaemon must tolerate the now-existing project row (idempotent
	// ensure-exists via ON CONFLICT DO NOTHING), proving the upsert seed behavior.
	d2, err := newDaemon(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("newDaemon(postgres, second run) should tolerate existing project: %v", err)
	}
	d2.Close()
}

func equalStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestResolveGates_DefaultIsBuildTestVet proves the daemon's DEFAULT verify recipe
// (no -recipe-dir) is the always-available Go toolchain trio: go build + go test +
// go vet (FIX #1). golangci-lint is intentionally absent from the default so the
// shipped daemon/container without it does not break.
func TestResolveGates_DefaultIsBuildTestVet(t *testing.T) {
	cfg := config{} // recipeDir empty -> default gates.
	gates, err := resolveGates(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("resolveGates(default): %v", err)
	}
	got := make([]string, len(gates))
	for i, g := range gates {
		got[i] = strings.Join(g.Argv, " ")
	}
	want := []string{"go build ./...", "go test ./...", "go vet ./..."}
	if !equalStrs(got, want) {
		t.Fatalf("default gates = %v, want %v", got, want)
	}
}

// TestResolveGates_NoConfigInDirFallsBack proves a -recipe-dir that has NO
// .conductor/config.yaml falls back to the default gates (backward compatible),
// not an error.
func TestResolveGates_NoConfigInDirFallsBack(t *testing.T) {
	cfg := config{recipeDir: t.TempDir()} // empty dir, no .conductor/config.yaml.
	gates, err := resolveGates(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("resolveGates(no config): %v", err)
	}
	if len(gates) != 3 {
		t.Fatalf("want 3 default gates when dir has no config, got %d: %+v", len(gates), gates)
	}
}

// TestResolveGates_FromConductorConfig proves a scaffolder-emitted
// .conductor/config.yaml UPGRADES the recipe: the daemon reads its verify gates
// (incl. an opt-in golangci-lint lint gate) directly from the config, closing the
// N-8 scaffolder→daemon recipe gap.
func TestResolveGates_FromConductorConfig(t *testing.T) {
	dir := t.TempDir()
	confDir := filepath.Join(dir, ".conductor")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	yaml := `version: 1
stack: go
base_branch: develop
recipe:
  develop: ["echo", "x"]
  verify:
    build: ["go", "build", "./..."]
    test: ["go", "test", "./..."]
    vet: ["go", "vet", "./..."]
    lint: ["golangci-lint", "run"]
`
	if err := os.WriteFile(filepath.Join(confDir, "config.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg := config{recipeDir: dir}
	gates, err := resolveGates(cfg, newTestLogger())
	if err != nil {
		t.Fatalf("resolveGates(config): %v", err)
	}
	got := make([]string, len(gates))
	for i, g := range gates {
		got[i] = strings.Join(g.Argv, " ")
	}
	want := []string{"go build ./...", "go test ./...", "go vet ./...", "golangci-lint run"}
	if !equalStrs(got, want) {
		t.Fatalf("config gates = %v, want %v (lint must be honored)", got, want)
	}
}
