package intake

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/everva/conductor-platform/internal/envsafe"
)

// TestClaudeArgs pins the argv: always `-p`, and `--model <m>` only when a model is pinned.
func TestClaudeArgs(t *testing.T) {
	if got := claudeArgs(claudeOptions{}); !equalStrings(got, []string{"-p"}) {
		t.Fatalf("no model: got %v, want [-p]", got)
	}
	if got := claudeArgs(claudeOptions{model: "claude-opus-4-8"}); !equalStrings(got, []string{"-p", "--model", "claude-opus-4-8"}) {
		t.Fatalf("with model: got %v, want [-p --model claude-opus-4-8]", got)
	}
	// A blank/whitespace model adds no flag (treated as "unset").
	if got := claudeArgs(claudeOptions{model: "  "}); !equalStrings(got, []string{"-p"}) {
		t.Fatalf("blank model: got %v, want [-p]", got)
	}
}

// TestClaudeEnv proves the Option-3 token discipline: a per-call token is injected into the
// RETURNED subprocess env only, never into the process's own os.Environ.
func TestClaudeEnv(t *testing.T) {
	// nil provider → the env is EXACTLY the sanitized inherited env; the provider path added nothing.
	before := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
	env, err := claudeEnv(context.Background(), claudeOptions{})
	if err != nil {
		t.Fatalf("nil provider: unexpected err %v", err)
	}
	if base := envsafe.Sanitize(os.Environ()); !equalStrings(env, base) {
		t.Fatalf("nil provider must return the sanitized env unchanged (len %d vs %d)", len(env), len(base))
	}

	// A provider's token is appended to the subprocess env — but os.Environ stays unchanged.
	env, err = claudeEnv(context.Background(), claudeOptions{
		tokenProvider: func(context.Context) (string, error) { return "sub-tok-123", nil },
	})
	if err != nil {
		t.Fatalf("provider: unexpected err %v", err)
	}
	if !contains(env, "CLAUDE_CODE_OAUTH_TOKEN=sub-tok-123") {
		t.Fatalf("provider token not injected into subprocess env: %v", lastTokenLine(env))
	}
	if os.Getenv("CLAUDE_CODE_OAUTH_TOKEN") != before {
		t.Fatalf("Option 3 VIOLATED: the process env CLAUDE_CODE_OAUTH_TOKEN was mutated (%q → %q)", before, os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"))
	}

	// An empty token from the provider injects nothing (the env's own auth still applies).
	env, err = claudeEnv(context.Background(), claudeOptions{
		tokenProvider: func(context.Context) (string, error) { return "", nil },
	})
	if err != nil {
		t.Fatalf("empty token: unexpected err %v", err)
	}
	if contains(env, "CLAUDE_CODE_OAUTH_TOKEN=") && lastTokenLine(env) == "CLAUDE_CODE_OAUTH_TOKEN=" {
		t.Fatalf("empty token must not add a blank token line")
	}

	// A provider ERROR aborts the run (a missing/unopenable credential is a distill failure,
	// not a silent unauthenticated call).
	_, err = claudeEnv(context.Background(), claudeOptions{
		tokenProvider: func(context.Context) (string, error) { return "", errors.New("no credential") },
	})
	if err == nil || !strings.Contains(err.Error(), "no credential") {
		t.Fatalf("provider error must propagate, got %v", err)
	}
}

// TestNewCommandDistillerVariants confirms both constructors build a usable *CommandDistiller
// (the zero config = NewCommandDistiller; with config = the Faz-R path) satisfying the seams.
func TestNewCommandDistillerVariants(t *testing.T) {
	var _ Distiller = NewCommandDistiller()
	d := NewCommandDistillerWithClaude(ClaudeDistillerConfig{
		TokenProvider: func(context.Context) (string, error) { return "tok", nil },
		Model:         "claude-opus-4-8",
	})
	if d == nil || d.run == nil || d.clarifyRun == nil || d.clarifyStreamRun == nil {
		t.Fatalf("NewCommandDistillerWithClaude must wire all three runners")
	}
}

// --- helpers ---

func equalStrings(a, b []string) bool {
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

func contains(env []string, entry string) bool {
	for _, e := range env {
		if e == entry {
			return true
		}
	}
	return false
}

func lastTokenLine(env []string) string {
	last := ""
	for _, e := range env {
		if strings.HasPrefix(e, "CLAUDE_CODE_OAUTH_TOKEN=") {
			last = e
		}
	}
	return last
}
