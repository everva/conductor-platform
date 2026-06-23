package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
)

// TestUsesClaudePerformer pins the Faz L2 detection that drives the CLAUDE_CODE_OAUTH_TOKEN
// startup hint: only a `claude` performer (by command base name) needs claude auth.
func TestUsesClaudePerformer(t *testing.T) {
	cases := []struct {
		name string
		argv []string
		want bool
	}{
		{"claude -p", []string{"claude", "-p"}, true},
		{"absolute path", []string{"/usr/local/bin/claude", "-p"}, true},
		{"non-claude performer", []string{"pnpm", "build"}, false},
		{"empty", nil, false},
		{"claude-like but not claude", []string{"claude-wrapper", "-p"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := usesClaudePerformer(tc.argv); got != tc.want {
				t.Errorf("usesClaudePerformer(%v) = %v, want %v", tc.argv, got, tc.want)
			}
		})
	}
}

// fakeFetcher records the kind requested and returns a fixed result.
type fakeFetcher struct {
	token   string
	found   bool
	err     error
	called  bool
	gotKind string
}

func (f *fakeFetcher) GetCredential(_ context.Context, kind string) (string, bool, error) {
	f.called = true
	f.gotKind = kind
	return f.token, f.found, f.err
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestEnsureClaudeAuth_EnvWinsNoFetch(t *testing.T) {
	t.Setenv(claudeCredentialKind, "sk-ant-oat-from-env")
	f := &fakeFetcher{}
	ensureClaudeAuth(context.Background(), f, []string{"claude", "-p"}, quietLogger())
	if f.called {
		t.Fatalf("must NOT fetch when the env var is already set")
	}
	if os.Getenv(claudeCredentialKind) != "sk-ant-oat-from-env" {
		t.Fatalf("env var must be left intact")
	}
}

func TestEnsureClaudeAuth_FetchFoundSetsEnv(t *testing.T) {
	t.Setenv(claudeCredentialKind, "") // baseline empty (+ cleanup restores after the test)
	f := &fakeFetcher{token: "sk-ant-oat-from-gateway", found: true}
	ensureClaudeAuth(context.Background(), f, []string{"claude", "-p"}, quietLogger())
	if !f.called || f.gotKind != claudeCredentialKind {
		t.Fatalf("must fetch the claude credential kind; called=%v kind=%q", f.called, f.gotKind)
	}
	if got := os.Getenv(claudeCredentialKind); got != "sk-ant-oat-from-gateway" {
		t.Fatalf("env var = %q, want the fetched token", got)
	}
}

func TestEnsureClaudeAuth_FetchNotFoundLeavesEnvEmpty(t *testing.T) {
	t.Setenv(claudeCredentialKind, "")
	f := &fakeFetcher{found: false}
	ensureClaudeAuth(context.Background(), f, []string{"claude", "-p"}, quietLogger())
	if got := os.Getenv(claudeCredentialKind); got != "" {
		t.Fatalf("env var must stay empty when nothing is stored, got %q", got)
	}
}

func TestEnsureClaudeAuth_FetchErrorIsNonFatal(t *testing.T) {
	t.Setenv(claudeCredentialKind, "")
	f := &fakeFetcher{err: errors.New("gateway 503")}
	ensureClaudeAuth(context.Background(), f, []string{"claude", "-p"}, quietLogger())
	if got := os.Getenv(claudeCredentialKind); got != "" {
		t.Fatalf("env var must stay empty on fetch error, got %q", got)
	}
}

func TestEnsureClaudeAuth_NonClaudePerformerNoOp(t *testing.T) {
	t.Setenv(claudeCredentialKind, "")
	f := &fakeFetcher{token: "x", found: true}
	ensureClaudeAuth(context.Background(), f, []string{"pnpm", "build"}, quietLogger())
	if f.called {
		t.Fatalf("must NOT fetch for a non-claude performer")
	}
}
