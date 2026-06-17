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
			if strings.TrimSpace(strings.TrimPrefix(trimmed, scheme)) == "" {
				return fmt.Errorf("hidden_holdout_ref %q has an external scheme but no locator body", ref)
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
