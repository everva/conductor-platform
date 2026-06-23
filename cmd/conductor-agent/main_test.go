package main

import "testing"

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
