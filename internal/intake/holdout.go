package intake

import (
	"fmt"
	"path/filepath"
	"strings"
)

// externalHoldoutSchemes are the locator schemes a holdout reference may use to
// point at the repo-EXTERNAL hidden holdout store (ADR-0018 §1: Faz-1a separate
// local dir / private ref; Faz-1b Postgres or a separate private repo). The
// reference is a POINTER only — the holdout body never lives in the public
// scenario.
//
//   - "store://"  — the platform holdout store (e.g. store://holdouts/A-1/...),
//     the form the existing tools/builder scenarios already use.
//   - "pg://"     — a Faz-1b Postgres-backed holdout row.
//   - "private:"  — a private out-of-repo git ref (e.g. private:holdouts#A-1).
var externalHoldoutSchemes = []string{"store://", "pg://", "private:"}

// validateHoldoutRef enforces the repo-external rule (ADR-0018): the hidden
// holdout must NOT be addressable inside the repo, so its reference must use a
// recognized external locator scheme and must not look like a repo-relative or
// absolute filesystem path that would place the holdout body in the checkout.
//
// It returns nil for a well-formed external reference, else a descriptive error
// (no scheme, or a repo-internal path) suitable for aggregation into a
// ValidationError.
func validateHoldoutRef(ref string) error {
	trimmed := strings.TrimSpace(ref)

	// A recognized external scheme is the accept path.
	for _, scheme := range externalHoldoutSchemes {
		if strings.HasPrefix(trimmed, scheme) {
			// Guard against an "empty" locator like "store://" with no body path.
			body := strings.TrimSpace(strings.TrimPrefix(trimmed, scheme))
			if body == "" {
				return fmt.Errorf("hidden_holdout_ref %q has an external scheme but no locator body", ref)
			}
			// S-4 (intake side): a private: ref's repo part must be a safe https/ssh
			// git remote — reject a file://, absolute-path, or "../"-traversal repo at
			// intake so an unsafe local-clone locator never reaches the store.
			if scheme == "private:" {
				if err := validatePrivateLocatorBody(ref, body); err != nil {
					return err
				}
			}
			return nil
		}
	}

	// Everything else is treated as a repo-internal path, which violates the
	// repo-external rule: an absolute path, or a relative path that resolves to a
	// file the performer would clone with the repo.
	if filepath.IsAbs(trimmed) {
		return fmt.Errorf("hidden_holdout_ref %q is an absolute path; the holdout must be repo-external (ADR-0018)", ref)
	}
	return fmt.Errorf(
		"hidden_holdout_ref %q is not a repo-external locator; use one of %s (ADR-0018)",
		ref, strings.Join(externalHoldoutSchemes, ", "),
	)
}

// validatePrivateLocatorBody tightens the private: scheme at intake (S-4): the body
// is "<repo>#<path>" and the <repo> part must NOT be a dangerous local-clone target —
// a file:// URL, an absolute local path, or a "../"-traversal — which would clone an
// arbitrary LOCAL directory (limited SSRF / local-fs read). It rejects only those
// clearly-unsafe shapes here (so the documented bare-slug example form keeps
// validating at intake); the holdout package's runtime guard additionally enforces
// the full https/ssh-only allowlist before any clone happens. The path part is not
// re-validated here (the store's path-traversal guard owns that).
func validatePrivateLocatorBody(ref, body string) error {
	repo := body
	if idx := strings.LastIndex(body, "#"); idx >= 0 {
		repo = strings.TrimSpace(body[:idx])
	}
	low := strings.ToLower(repo)
	switch {
	case strings.HasPrefix(low, "file://"):
		return fmt.Errorf("hidden_holdout_ref %q private repo uses file:// (local clone forbidden; use an https/ssh git remote) (ADR-0017/0018)", ref)
	case strings.HasPrefix(repo, "/") || isWindowsAbs(repo):
		return fmt.Errorf("hidden_holdout_ref %q private repo is an absolute local path (local clone forbidden; use an https/ssh git remote) (ADR-0017/0018)", ref)
	case repo == ".." || strings.HasPrefix(repo, "../") || strings.Contains(repo, "/../") || strings.HasSuffix(repo, "/.."):
		return fmt.Errorf("hidden_holdout_ref %q private repo contains a '..' path segment (forbidden) (ADR-0017/0018)", ref)
	}
	return nil
}

// isWindowsAbs reports whether repo looks like an absolute Windows path
// ("C:\\..." or "C:/...") so it is rejected as a local clone target.
func isWindowsAbs(repo string) bool {
	if len(repo) < 3 {
		return false
	}
	c := repo[0]
	isLetter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
	return isLetter && repo[1] == ':' && (repo[2] == '\\' || repo[2] == '/')
}
