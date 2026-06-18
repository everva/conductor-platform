// Package holdout provides a concrete, filesystem-backed implementation of the
// verify.HoldoutStore interface (ADR-0018): the repo-EXTERNAL source of hidden
// holdouts the independent verify gate injects into its throwaway verify-worktree.
//
// The store resolves a scenario's locator (statestore.Scenario.HoldoutRef, e.g.
// "store://holdouts/A-1/spec.yaml") to a directory UNDER an operator-configured
// root that lives OUTSIDE any product repo, reads the holdout's injectable files
// there, and returns them as a verify.Holdout for temporary injection. It is the
// Faz-1a backing the daemon wires when -holdout-store is set; Faz-1b can swap a
// Postgres or private-repo backing behind the same verify.HoldoutStore interface
// without touching the verifier (the pg:// and private: schemes are reserved and
// currently return a clear unsupported error).
//
// SECURITY (ADR-0018):
//   - The root is repo-EXTERNAL by construction (an operator-supplied directory),
//     so the holdout body never lives in the public checkout.
//   - Stored file names are sanitized so a malicious holdout cannot inject OUTSIDE
//     the verify-worktree (".." and absolute paths are rejected). The verifier
//     applies the same guard as defense-in-depth.
//   - Holdout file CONTENTS are hidden: this package reads them but never logs them.
package holdout

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/everva/conductor-platform/internal/verify"
)

// storeScheme is the only locator scheme the filesystem store resolves in Faz-1a.
// pg:// and private: are recognized-but-unsupported (a clear TODO error) so the
// daemon fails loudly rather than silently skipping a configured holdout.
const storeScheme = "store://"

// pgScheme / privateScheme are the reserved Faz-1b schemes (ADR-0018). They are
// recognized so Fetch returns a clear "unsupported scheme" error instead of
// mistaking them for a relative path; a future Postgres/private-repo store
// implements them behind the same verify.HoldoutStore interface.
const (
	pgScheme      = "pg://"
	privateScheme = "private:"
)

// injectSubdir is the per-holdout subdirectory whose contents are injected into
// the verify-worktree. The layout under the configured root is:
//
//	<root>/<locator-path>/inject/...      -> injected at its path under the worktree
//
// where <locator-path> is the directory part of the locator body (everything in
// "store://<body>" up to the last path element). For a locator like
// "store://holdouts/A-1/spec.yaml" the holdout directory is
// "<root>/holdouts/A-1/" and every file under "<root>/holdouts/A-1/inject/" is
// injected at its path RELATIVE to that inject/ dir. This keeps a holdout's own
// metadata (e.g. spec.yaml) OUT of the injected set: only inject/ is copied into
// the verify-worktree, so non-test fixtures the holdout author keeps alongside
// the spec never leak into the reviewed checkout.
const injectSubdir = "inject"

// FSStore is a filesystem-backed verify.HoldoutStore rooted at a repo-EXTERNAL
// directory. Construct it with New; it holds only the root path and no other
// state, so it is safe to share across verify runs.
type FSStore struct {
	// root is the repo-external directory holdouts are resolved under.
	root string
}

// New returns an FSStore rooted at root, the repo-external directory holdouts are
// resolved under. root must be non-empty; the daemon only constructs an FSStore
// when an operator supplies -holdout-store, so an empty root is a programming
// error here (the daemon keeps the noop store when no root is configured).
func New(root string) (*FSStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("holdout: empty store root")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("holdout: resolve store root %q: %w", root, err)
	}
	return &FSStore{root: abs}, nil
}

// Root returns the resolved (absolute) store root. It is exposed so the daemon
// can log WHICH holdout root is active (the root path is operator-supplied
// configuration, not a secret); it never exposes holdout file contents.
func (s *FSStore) Root() string { return s.root }

// Fetch resolves ref to a holdout under the store root and returns its injectable
// files (verify.HoldoutStore). It accepts the "store://" scheme; "pg://" and
// "private:" are recognized but currently unsupported (a clear TODO error). An
// empty ref means the scenario has no holdout: Fetch returns an empty holdout so
// the verifier injects nothing — non-holdout projects work unchanged. A missing
// holdout directory, or a stored file name that would escape the worktree, is a
// clear error rather than a silent skip.
func (s *FSStore) Fetch(_ context.Context, ref string) (verify.Holdout, error) {
	trimmed := strings.TrimSpace(ref)

	// An empty locator means "no holdout for this scenario": skip cleanly. The
	// conductor passes an empty ref for tasks whose scenario has no HoldoutRef (or
	// no scenario at all), so a non-holdout project verifies on its public gates
	// alone without erroring.
	if trimmed == "" {
		return verify.Holdout{}, nil
	}

	switch {
	case strings.HasPrefix(trimmed, pgScheme), strings.HasPrefix(trimmed, privateScheme):
		return verify.Holdout{}, fmt.Errorf("holdout: unsupported scheme in %q (TODO: pg:// and private: are Faz-1b)", ref)
	case !strings.HasPrefix(trimmed, storeScheme):
		return verify.Holdout{}, fmt.Errorf("holdout: unrecognized locator %q (want %s, %s, or %s)", ref, storeScheme, pgScheme, privateScheme)
	}

	body := strings.TrimPrefix(trimmed, storeScheme)
	if body == "" {
		return verify.Holdout{}, fmt.Errorf("holdout: %q has scheme but no locator body", ref)
	}

	// The locator addresses a file within the holdout (e.g.
	// holdouts/A-1/spec.yaml); the holdout DIRECTORY is that file's parent. Resolve
	// the directory under the root, then inject everything under its inject/ subdir.
	dir, name := holdoutDir(body)
	if dir == "" {
		return verify.Holdout{}, fmt.Errorf("holdout: locator %q has no holdout directory (want store://<dir>/<file>)", ref)
	}

	// Defense-in-depth: the locator body must not escape the root via "..".
	cleanDir := filepath.Clean(dir)
	if cleanDir == ".." || strings.HasPrefix(cleanDir, ".."+string(os.PathSeparator)) || filepath.IsAbs(cleanDir) {
		return verify.Holdout{}, fmt.Errorf("holdout: locator %q escapes the store root", ref)
	}

	holdoutDirAbs := filepath.Join(s.root, filepath.FromSlash(cleanDir))
	injectDir := filepath.Join(holdoutDirAbs, injectSubdir)

	info, err := os.Stat(injectDir)
	if err != nil {
		if os.IsNotExist(err) {
			return verify.Holdout{}, fmt.Errorf("holdout: no inject dir for %q at %s", ref, injectDir)
		}
		return verify.Holdout{}, fmt.Errorf("holdout: stat inject dir for %q: %w", ref, err)
	}
	if !info.IsDir() {
		return verify.Holdout{}, fmt.Errorf("holdout: inject path for %q is not a directory: %s", ref, injectDir)
	}

	files, err := readInjectFiles(injectDir)
	if err != nil {
		return verify.Holdout{}, fmt.Errorf("holdout: read %q: %w", ref, err)
	}
	if len(files) == 0 {
		return verify.Holdout{}, fmt.Errorf("holdout: inject dir for %q is empty: %s", ref, injectDir)
	}

	// Name is a non-revealing identifier for findings: the holdout's directory id
	// (its last path element), never the file contents. e.g. "A-1".
	return verify.Holdout{Name: holdoutName(cleanDir, name), Files: files}, nil
}

// holdoutDir splits a locator body (slash-separated, e.g. "holdouts/A-1/spec.yaml")
// into its directory ("holdouts/A-1") and final element ("spec.yaml"). A body with
// no slash (a bare file) yields an empty dir, which Fetch rejects: the layout
// requires a holdout directory so the inject/ subdir has a home.
func holdoutDir(body string) (dir, name string) {
	body = strings.Trim(body, "/")
	idx := strings.LastIndex(body, "/")
	if idx < 0 {
		return "", body
	}
	return body[:idx], body[idx+1:]
}

// holdoutName returns a stable, non-revealing identifier for the holdout used in
// findings: the last element of the holdout directory (e.g. "A-1" from
// "holdouts/A-1"), falling back to the locator's file name. It never includes
// file contents.
func holdoutName(dir, file string) string {
	dir = strings.TrimRight(filepath.ToSlash(dir), "/")
	if idx := strings.LastIndex(dir, "/"); idx >= 0 {
		if last := dir[idx+1:]; last != "" {
			return last
		}
	}
	if dir != "" && dir != "." {
		return dir
	}
	return file
}

// readInjectFiles walks injectDir and returns a map of worktree-relative path
// (slash-separated, RELATIVE to injectDir) to file contents. It rejects any name
// that would escape the verify-worktree (absolute or containing ".."), so a
// malicious holdout cannot write outside its sandbox (ADR-0018). Symlinks are not
// followed for their target as a regular file: a symlink entry is rejected to
// avoid an injected link pointing outside the worktree.
func readInjectFiles(injectDir string) (map[string][]byte, error) {
	files := make(map[string][]byte)
	walkErr := filepath.WalkDir(injectDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Reject symlinks: an injected symlink could resolve outside the worktree.
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink not allowed in holdout: %s", path)
		}
		rel, relErr := filepath.Rel(injectDir, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		// Security: the relative path must stay inside the worktree. WalkDir under a
		// real directory cannot produce ".." here, but we guard explicitly so the
		// contract is enforced regardless of how the tree was authored.
		if rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
			return fmt.Errorf("holdout file escapes worktree: %q", rel)
		}
		content, readErr := os.ReadFile(path) //nolint:gosec // path is under the operator-supplied repo-external holdout root.
		if readErr != nil {
			return readErr
		}
		files[rel] = content
		return nil
	})
	if walkErr != nil {
		return nil, walkErr
	}
	return files, nil
}
