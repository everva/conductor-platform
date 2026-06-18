// Package envsafe holds the single, shared secret-env sanitizer used wherever
// the daemon spawns a subprocess over attacker-influenced repo content: the
// performer (engine.execRunner, S-1) and the verify gate/holdout
// (verify.sanitizedEnv, S-2). It is a leaf package with no internal deps so both
// callers can import it without an import cycle (engine.go's interface/types
// stay frozen; this is additive, ADR-0021).
//
// WHY A DENYLIST, NOT A PATTERN-STRIP. The performer is `claude -p` and the
// gates are `go build`/`go test`/`npm test`/holdout — they need a broad,
// unpredictable inherited environment to function: PATH, HOME, the Go/Node
// toolchain vars (GOCACHE, GOPATH, …), locale, AND the Claude auth env
// (CLAUDE_CODE_OAUTH_TOKEN / other CLAUDE_*). An aggressive "strip anything
// shaped like *_TOKEN/*_SECRET" would kill that OAuth token and break the
// performer. So instead we remove ONLY the daemon's OWN known secrets and leave
// everything else intact:
//
//   - GH_TOKEN, GITHUB_TOKEN — repo-write GitHub creds. The performer/gates do
//     NOT need them: git auth is a credential-helper baked into .git/config by
//     the provisioner (daemon-side), and push/merge happen daemon-side too.
//   - every CONDUCTOR_* var — the daemon's own config, including CONDUCTOR_DSN
//     (the Postgres password). The subprocess must never see the daemon's DSN or
//     other config; leaking it both exposes the secret AND could silently change
//     a gated project's behavior (a fake-red).
//
// Everything not on the denylist (PATH, HOME, GO*, CLAUDE_*, locale, arbitrary
// user vars) is KEPT.
package envsafe

import "strings"

// denyExact are the daemon's own secret env keys removed by exact (full-key)
// match. These are repo-write GitHub tokens the subprocess has no need for.
var denyExact = map[string]struct{}{
	"GH_TOKEN":     {},
	"GITHUB_TOKEN": {},
}

// denyPrefix are the env-key prefixes removed by prefix match. CONDUCTOR_ covers
// the entire daemon config surface (CONDUCTOR_DSN and any other CONDUCTOR_* var)
// without having to enumerate each one.
var denyPrefix = []string{"CONDUCTOR_"}

// Sanitize returns a copy of environ ("KEY=VALUE" entries) with the daemon's own
// secrets removed: the exact keys GH_TOKEN/GITHUB_TOKEN and every CONDUCTOR_*
// var. Every other entry — PATH, HOME, GO*, CLAUDE_* (the Claude OAuth token),
// locale, etc. — is preserved so the performer and the gates still work. The
// input slice is not mutated.
func Sanitize(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		if denied(kv) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// denied reports whether the "KEY=VALUE" entry's KEY is on the denylist. An
// entry with no "=" is treated as a bare key.
func denied(kv string) bool {
	key := kv
	if i := strings.IndexByte(kv, '='); i >= 0 {
		key = kv[:i]
	}
	if _, ok := denyExact[key]; ok {
		return true
	}
	for _, p := range denyPrefix {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}
