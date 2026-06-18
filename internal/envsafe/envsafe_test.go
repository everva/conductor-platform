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
