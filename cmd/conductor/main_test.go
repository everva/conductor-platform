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
	"testing"
	"time"
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
