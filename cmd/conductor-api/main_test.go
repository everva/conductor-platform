package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// TestRunRefusesWithoutToken asserts the safety default: an empty
// CONDUCTOR_API_TOKEN causes run to refuse to start (exit code 2) with a clear
// message, never binding a socket.
func TestRunRefusesWithoutToken(t *testing.T) {
	t.Setenv("CONDUCTOR_API_TOKEN", "")
	var stderr bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&stderr, nil))

	code := run(context.Background(), nil, logger, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "CONDUCTOR_API_TOKEN required") {
		t.Errorf("stderr = %q, want refusal message", stderr.String())
	}
	if !strings.Contains(stderr.String(), "refusing to run an unauthenticated control plane") {
		t.Errorf("stderr missing safety wording: %q", stderr.String())
	}
}

// TestRunBadFlagExits2 asserts a parse error returns exit code 2.
func TestRunBadFlagExits2(t *testing.T) {
	t.Setenv("CONDUCTOR_API_TOKEN", "x")
	var stderr bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&stderr, nil))

	code := run(context.Background(), []string{"-nonexistent-flag"}, logger, &stderr)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

// TestParseConfigDefaults verifies addr default and env-only token sourcing.
func TestParseConfigDefaults(t *testing.T) {
	t.Setenv("CONDUCTOR_API_TOKEN", "env-token")
	t.Setenv("CONDUCTOR_API_ADDR", "")
	t.Setenv("CONDUCTOR_DSN", "")

	cfg, err := parseConfig(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.addr != ":8080" {
		t.Errorf("addr = %q, want :8080", cfg.addr)
	}
	if cfg.dsn != "" {
		t.Errorf("dsn = %q, want empty", cfg.dsn)
	}
	if cfg.token != "env-token" {
		t.Errorf("token = %q, want env-token", cfg.token)
	}
}

// TestStoreBackendName ensures the DSN→name mapping never echoes the DSN.
func TestStoreBackendName(t *testing.T) {
	if got := storeBackendName(""); got != "memory" {
		t.Errorf("empty dsn → %q, want memory", got)
	}
	if got := storeBackendName("postgres://secret"); got != "postgres" {
		t.Errorf("non-empty dsn → %q, want postgres", got)
	}
}
