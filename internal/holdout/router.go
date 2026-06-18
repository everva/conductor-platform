package holdout

import (
	"context"
	"fmt"
	"strings"

	"github.com/everva/conductor-platform/internal/verify"
)

// Router is a scheme-routing verify.HoldoutStore (ADR-0018, Faz-1.5-a). It parses
// a holdout locator's scheme and dispatches to the matching backing sub-store:
//
//	store://...    -> FSStore           (repo-EXTERNAL filesystem root)
//	pg://...       -> PGStore           (repo-EXTERNAL central Postgres table)
//	private:...    -> PrivateRepoStore  (repo-EXTERNAL separate git repo)
//
// All three backings are repo-EXTERNAL by construction. A backing is OPTIONAL:
// the daemon configures only the ones an operator wired. A locator whose scheme
// has no configured backing is a CLEAR runtime error ("scheme X not configured"),
// NOT a silent skip — the daemon fails loudly rather than letting a missing
// holdout pass unverified (Rule#9, no fake-green). An empty locator means the
// scenario carries no holdout: Fetch returns an empty holdout so a non-holdout
// project verifies on its public gates alone (matching the existing behavior).
type Router struct {
	fs      *FSStore
	pg      *PGStore
	private *PrivateRepoStore
}

// Compile-time assertion that *Router satisfies the frozen verify.HoldoutStore.
var _ verify.HoldoutStore = (*Router)(nil)

// RouterOption configures a Router backing. Each option is additive; an unset
// backing leaves its scheme unconfigured (a locator for it is a clear error).
type RouterOption func(*Router)

// WithFS wires the store:// filesystem backing.
func WithFS(s *FSStore) RouterOption { return func(r *Router) { r.fs = s } }

// WithPG wires the pg:// Postgres backing.
func WithPG(s *PGStore) RouterOption { return func(r *Router) { r.pg = s } }

// WithPrivate wires the private: git-repo backing.
func WithPrivate(s *PrivateRepoStore) RouterOption { return func(r *Router) { r.private = s } }

// NewRouter builds a scheme-routing store from the supplied backing options. At
// least one backing must be configured; a Router with no backings would error on
// every non-empty locator, which is a programming error at the call site.
func NewRouter(opts ...RouterOption) (*Router, error) {
	r := &Router{}
	for _, o := range opts {
		o(r)
	}
	if r.fs == nil && r.pg == nil && r.private == nil {
		return nil, fmt.Errorf("holdout: router needs at least one configured backing")
	}
	return r, nil
}

// ActiveSchemes returns the configured scheme names (store, pg, private) for
// operator-facing logging. It exposes only scheme names — never roots, the DSN,
// or any token — so it is safe to log.
func (r *Router) ActiveSchemes() []string {
	var out []string
	if r.fs != nil {
		out = append(out, "store")
	}
	if r.pg != nil {
		out = append(out, "pg")
	}
	if r.private != nil {
		out = append(out, "private")
	}
	return out
}

// Fetch parses ref's scheme and dispatches to the matching backing. An empty ref
// skips cleanly (empty holdout). An unknown scheme, or a known scheme whose
// backing is not configured, is a clear error.
func (r *Router) Fetch(ctx context.Context, ref string) (verify.Holdout, error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return verify.Holdout{}, nil
	}

	switch {
	case strings.HasPrefix(trimmed, storeScheme):
		if r.fs == nil {
			return verify.Holdout{}, fmt.Errorf("holdout: scheme %q not configured for ref %q", "store://", ref)
		}
		return r.fs.Fetch(ctx, ref)
	case strings.HasPrefix(trimmed, pgScheme):
		if r.pg == nil {
			return verify.Holdout{}, fmt.Errorf("holdout: scheme %q not configured for ref %q", "pg://", ref)
		}
		return r.pg.Fetch(ctx, ref)
	case strings.HasPrefix(trimmed, privateScheme):
		if r.private == nil {
			return verify.Holdout{}, fmt.Errorf("holdout: scheme %q not configured for ref %q", "private:", ref)
		}
		return r.private.Fetch(ctx, ref)
	default:
		return verify.Holdout{}, fmt.Errorf("holdout: unrecognized locator %q (want %s, %s, or %s)", ref, storeScheme, pgScheme, privateScheme)
	}
}
