// Package main daemon tests for the A.1 hidden-holdout wiring (ADR-0018): the
// -holdout-store / -holdout-cmd flags and the newVerifier mode selection. They are
// hermetic and offline — they assert config parsing and that newVerifier returns a
// usable verifier in both the default (noop) and configured (fs-store) modes.
package main

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestParseConfig_Holdout(t *testing.T) {
	t.Run("defaults: no store, go-test cmd", func(t *testing.T) {
		cfg, err := parseConfig([]string{"-project", "p1", "-root", "/tmp/r"}, io.Discard)
		if err != nil {
			t.Fatalf("parseConfig: %v", err)
		}
		if cfg.holdoutStore != "" {
			t.Fatalf("holdoutStore = %q, want empty by default (noop holdout, backward compatible)", cfg.holdoutStore)
		}
		if got := strings.Join(cfg.holdoutCmd, " "); got != defaultHoldoutCmd {
			t.Fatalf("holdoutCmd = %q, want default %q", got, defaultHoldoutCmd)
		}
	})

	t.Run("store + cmd from flags", func(t *testing.T) {
		cfg, err := parseConfig([]string{
			"-project", "p1", "-root", "/tmp/r",
			"-holdout-store", "/srv/holdouts", "-holdout-cmd", "go test ./internal/...",
		}, io.Discard)
		if err != nil {
			t.Fatalf("parseConfig: %v", err)
		}
		if cfg.holdoutStore != "/srv/holdouts" {
			t.Fatalf("holdoutStore = %q, want /srv/holdouts", cfg.holdoutStore)
		}
		if got := strings.Join(cfg.holdoutCmd, " "); got != "go test ./internal/..." {
			t.Fatalf("holdoutCmd = %q, want %q", got, "go test ./internal/...")
		}
	})

	t.Run("store + cmd from env", func(t *testing.T) {
		t.Setenv("CONDUCTOR_HOLDOUT_STORE", "/env/holdouts")
		t.Setenv("CONDUCTOR_HOLDOUT_CMD", "make holdout")
		cfg, err := parseConfig([]string{"-project", "p1", "-root", "/tmp/r"}, io.Discard)
		if err != nil {
			t.Fatalf("parseConfig: %v", err)
		}
		if cfg.holdoutStore != "/env/holdouts" {
			t.Fatalf("holdoutStore = %q, want /env/holdouts from env", cfg.holdoutStore)
		}
		if got := strings.Join(cfg.holdoutCmd, " "); got != "make holdout" {
			t.Fatalf("holdoutCmd = %q, want %q from env", got, "make holdout")
		}
	})

	t.Run("store set but cmd empty is rejected", func(t *testing.T) {
		_, err := parseConfig([]string{
			"-project", "p1", "-root", "/tmp/r",
			"-holdout-store", "/srv/holdouts", "-holdout-cmd", "   ",
		}, io.Discard)
		if err == nil {
			t.Fatalf("empty -holdout-cmd with -holdout-store must be rejected")
		}
	})
}

// TestNewVerifier_ModeSelection proves the backward-compatible default (no store =
// noop verifier) and the configured path (fs-store verifier) both build a usable
// *verify.Verifier without error. An empty root must not error (it selects noop);
// a non-empty root selects the filesystem store.
func TestNewVerifier_ModeSelection(t *testing.T) {
	t.Run("no backing: noop (backward compatible)", func(t *testing.T) {
		cfg := config{holdoutCmd: []string{"true"}} // no store/dsn/private-cache.
		v, closer, err := newVerifier(context.Background(), cfg, newTestLogger())
		if err != nil {
			t.Fatalf("newVerifier (noop): %v", err)
		}
		defer closer()
		if v == nil {
			t.Fatal("newVerifier returned nil verifier in noop mode")
		}
	})

	t.Run("fs store configured: router (backward compatible)", func(t *testing.T) {
		// -holdout-store alone still works (fs-only router), no DSN, no private cache.
		cfg := config{holdoutStore: t.TempDir(), holdoutCmd: []string{"go", "test", "./..."}}
		v, closer, err := newVerifier(context.Background(), cfg, newTestLogger())
		if err != nil {
			t.Fatalf("newVerifier (fs router): %v", err)
		}
		defer closer()
		if v == nil {
			t.Fatal("newVerifier returned nil verifier in fs mode")
		}
	})

	t.Run("private cache configured: router (no dsn, no token)", func(t *testing.T) {
		cfg := config{holdoutPrivateCache: t.TempDir(), holdoutCmd: []string{"true"}}
		v, closer, err := newVerifier(context.Background(), cfg, newTestLogger())
		if err != nil {
			t.Fatalf("newVerifier (private router): %v", err)
		}
		defer closer()
		if v == nil {
			t.Fatal("newVerifier returned nil verifier in private mode")
		}
	})
}

// TestParseConfig_HoldoutPrivate proves the private: backing config parses: the
// cache dir from the flag and the gh-token from CONDUCTOR_GH_TOKEN (then GH_TOKEN).
func TestParseConfig_HoldoutPrivate(t *testing.T) {
	t.Run("cache flag + CONDUCTOR_GH_TOKEN", func(t *testing.T) {
		t.Setenv("CONDUCTOR_GH_TOKEN", "ghp_x")
		cfg, err := parseConfig([]string{
			"-project", "p1", "-root", "/tmp/r",
			"-holdout-private-cache", "/srv/cache",
		}, io.Discard)
		if err != nil {
			t.Fatalf("parseConfig: %v", err)
		}
		if cfg.holdoutPrivateCache != "/srv/cache" {
			t.Fatalf("holdoutPrivateCache = %q", cfg.holdoutPrivateCache)
		}
		if cfg.holdoutGHToken != "ghp_x" {
			t.Fatalf("holdoutGHToken not read from CONDUCTOR_GH_TOKEN")
		}
	})

	t.Run("falls back to GH_TOKEN", func(t *testing.T) {
		t.Setenv("GH_TOKEN", "ghp_fallback")
		cfg, err := parseConfig([]string{"-project", "p1", "-root", "/tmp/r"}, io.Discard)
		if err != nil {
			t.Fatalf("parseConfig: %v", err)
		}
		if cfg.holdoutGHToken != "ghp_fallback" {
			t.Fatalf("holdoutGHToken = %q, want GH_TOKEN fallback", cfg.holdoutGHToken)
		}
	})
}
