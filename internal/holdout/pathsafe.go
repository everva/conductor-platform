package holdout

import (
	"fmt"
	"path/filepath"
	"strings"
)

// safeRel reports an error if rel (a slash-separated, worktree-relative inject
// path) would escape the verify-worktree once injected. A holdout — from ANY
// backing (filesystem, Postgres, or a private repo) — must never write outside
// its sandbox (ADR-0018), so every store validates each injected path through
// this single shared guard: absolute paths and any path containing a ".."
// element (or the bare "..") are rejected. The verifier applies the same guard
// as defense-in-depth.
func safeRel(rel string) error {
	clean := filepath.ToSlash(rel)
	if clean == "" {
		return fmt.Errorf("holdout: empty inject path")
	}
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "/") {
		return fmt.Errorf("holdout: inject path is absolute: %q", rel)
	}
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") || strings.HasSuffix(clean, "/..") {
		return fmt.Errorf("holdout: inject path escapes worktree: %q", rel)
	}
	return nil
}
