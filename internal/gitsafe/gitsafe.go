// Package gitsafe validates user-supplied values that later become POSITIONAL git
// arguments (repository URLs/paths, branch names) so a value beginning with "-"
// cannot be misparsed by git as an option — e.g. an onboarded repo or base branch
// of "--upload-pack=<cmd>" would turn `git clone`/`git fetch` into arbitrary
// command execution — and embedded whitespace/control characters cannot smuggle
// extra tokens. It is the shared, caller-side guard; the provisioner additionally
// places a literal `--` before positionals at the exec site as defense in depth.
package gitsafe

import "strings"

// ValidArg reports whether s is safe to hand to git as a positional argument. It
// must be non-empty, must not begin with "-" (option injection), and must contain
// no whitespace or control characters. It deliberately still permits owner/name,
// https/ssh URLs, and absolute local paths.
func ValidArg(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == ' ' || r == 0x7f {
			return false
		}
	}
	return true
}
