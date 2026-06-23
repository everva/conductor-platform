package envsafe

import (
	"strings"
	"testing"
)

// TestSanitize_DenylistsDaemonSecretsKeepsRest proves the shared sanitizer
// removes ONLY the daemon's own secrets (GH_TOKEN, GITHUB_TOKEN, every
// CONDUCTOR_*) and keeps everything the performer/gates need — including the
// Claude OAuth token (denylist, not aggressive pattern-strip).
func TestSanitize_DenylistsDaemonSecretsKeepsRest(t *testing.T) {
	in := []string{
		"GH_TOKEN=ghp_faketokenvalue",
		"GITHUB_TOKEN=ghp_anotherfake",
		"CONDUCTOR_DSN=postgres://leak:leak@host/db",
		"CONDUCTOR_FOO=bar",
		"PATH=/usr/bin:/bin",
		"HOME=/home/runner",
		"CLAUDE_CODE_OAUTH_TOKEN=claude-oauth-fake",
		"GOCACHE=/tmp/gocache",
		"MY_VAR=keepme",
	}

	got := Sanitize(in)
	gotKeys := map[string]string{}
	for _, kv := range got {
		if i := strings.IndexByte(kv, '='); i >= 0 {
			gotKeys[kv[:i]] = kv[i+1:]
		}
	}

	removed := []string{"GH_TOKEN", "GITHUB_TOKEN", "CONDUCTOR_DSN", "CONDUCTOR_FOO"}
	for _, k := range removed {
		if _, ok := gotKeys[k]; ok {
			t.Errorf("%s must be stripped (daemon secret), but survived", k)
		}
	}

	kept := map[string]string{
		"PATH":                    "/usr/bin:/bin",
		"HOME":                    "/home/runner",
		"CLAUDE_CODE_OAUTH_TOKEN": "claude-oauth-fake",
		"GOCACHE":                 "/tmp/gocache",
		"MY_VAR":                  "keepme",
	}
	for k, want := range kept {
		if v, ok := gotKeys[k]; !ok {
			t.Errorf("%s must be kept (performer/gate needs it), but was stripped", k)
		} else if v != want {
			t.Errorf("%s value = %q, want %q (value must be preserved verbatim)", k, v, want)
		}
	}
}

// TestSanitize_PreservesClaudeOAuthToken pins the Faz L1↔L2 contract (ADR-0049): the
// portable claude OAuth token the editor mints (`claude setup-token`) is delivered to the
// performer via the CLAUDE_CODE_OAUTH_TOKEN env var, which the headless `claude -p` reads.
// The performer subprocess inherits Sanitize(os.Environ()) (command_engine.execRunner), so
// this var MUST survive sanitization. A regression here (e.g. someone adding a CLAUDE_/*_TOKEN
// prefix to the denylist) would silently break editor-mediated login — fail loudly instead.
func TestSanitize_PreservesClaudeOAuthToken(t *testing.T) {
	const tok = "sk-ant-oat-fake-do-not-strip"
	got := Sanitize([]string{
		"CLAUDE_CODE_OAUTH_TOKEN=" + tok,
		"GH_TOKEN=ghp_stripme",
	})
	var kept bool
	for _, kv := range got {
		if kv == "CLAUDE_CODE_OAUTH_TOKEN="+tok {
			kept = true
		}
		if strings.HasPrefix(kv, "GH_TOKEN=") {
			t.Errorf("GH_TOKEN must be stripped, but survived")
		}
	}
	if !kept {
		t.Fatalf("CLAUDE_CODE_OAUTH_TOKEN must be preserved verbatim (Faz L2 depends on it), but was stripped")
	}
}

// TestSanitize_DoesNotMutateInput proves the input slice is left untouched.
func TestSanitize_DoesNotMutateInput(t *testing.T) {
	in := []string{"GH_TOKEN=ghp_fake", "PATH=/bin"}
	before := append([]string(nil), in...)
	_ = Sanitize(in)
	for i := range in {
		if in[i] != before[i] {
			t.Fatalf("Sanitize mutated input: in[%d] = %q, want %q", i, in[i], before[i])
		}
	}
}
