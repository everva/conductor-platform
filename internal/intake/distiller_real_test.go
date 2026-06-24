package intake

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/everva/conductor-platform/internal/envsafe"
)

// TestStyleDirective proves the additive output directive: Turkish + single-scenario when
// requested, EMPTY when neither (the frozen prompts behave exactly as before).
func TestStyleDirective(t *testing.T) {
	// Default off → no directive (frozen behavior preserved).
	if d := styleDirective(claudeOptions{}); d != "" {
		t.Fatalf("zero opts must yield no directive, got %q", d)
	}
	// Turkish → instructs Turkish title/acceptance.
	d := styleDirective(claudeOptions{lang: "tr"})
	if !strings.Contains(d, "TURKISH") || !strings.Contains(strings.ToLower(d), "title") {
		t.Fatalf("tr directive missing Turkish title rule: %q", d)
	}
	// Single → forbids splitting a refactor/removal.
	d = styleDirective(claudeOptions{single: true})
	if !strings.Contains(d, "EXACTLY ONE") || !strings.Contains(d, "NEVER split") {
		t.Fatalf("single directive missing the no-split rule: %q", d)
	}
	// English (non-tr) lang alone adds no Turkish rule.
	if d := styleDirective(claudeOptions{lang: "en"}); strings.Contains(d, "TURKISH") {
		t.Fatalf("non-tr lang must not request Turkish: %q", d)
	}
}

// TestWithDirective proves the directive is injected INSIDE the rules (before "Conversation:"),
// and that a directive-free options set returns the prompt byte-identical (frozen).
func TestWithDirective(t *testing.T) {
	base := "RULES:\n- foo\n\nConversation:\n"
	// No style → unchanged.
	if got := withDirective(base, claudeOptions{}); got != base {
		t.Fatalf("no-style must return the prompt unchanged")
	}
	// With style → directive lands before the Conversation marker, conversation section intact.
	got := withDirective(base, claudeOptions{lang: "tr", single: true})
	conv := strings.Index(got, "\nConversation:")
	rules := strings.Index(got, "TURKISH")
	if rules < 0 || conv < 0 || rules > conv {
		t.Fatalf("directive must be injected before Conversation: got %q", got)
	}
	if !strings.HasSuffix(got, "Conversation:\n") {
		t.Fatalf("the Conversation marker must remain at the end: %q", got)
	}
}

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
