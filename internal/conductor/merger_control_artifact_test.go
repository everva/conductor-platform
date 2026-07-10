package conductor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/statestore"
)

// setupControlArtifactConflict builds the EXACT topology that wedged xirigo-vendor's
// V-44-help-article: the base branch once TRACKED a conductor control file, a task
// branch was cut while it existed and then MODIFIED it (a strict-review round rewrote
// .conductor/REVIEW.md and the performer committed it), and afterwards the base DELETED
// the file (a later task's fresh worktree did not have it, so its `git add -A` recorded
// the deletion). Squashing that branch yields:
//
//	CONFLICT (modify/delete): <path> deleted in HEAD and modified in <branch>
//
// Every retry reproduces it identically. extra, when non-empty, is a second file the
// task branch also touches (product content) so the merge has real work to land.
func setupControlArtifactConflict(t *testing.T, ctrlPath string) (clone, base, branch string) {
	t.Helper()
	base, branch = "develop", "conductor/proj-1/T-44"

	clone = t.TempDir()
	gitT(t, clone, "init", "-q", "-b", base)
	gitT(t, clone, "config", "user.name", "test")
	gitT(t, clone, "config", "user.email", "test@test")
	write(t, clone, "product.txt", "v1\n")
	write(t, clone, ctrlPath, "findings: round 1\n")
	gitT(t, clone, "add", ".")
	gitT(t, clone, "commit", "-q", "-m", "base with control artifact tracked")

	// Task branch: rewrite the control file (a review round) + do real product work.
	gitT(t, clone, "checkout", "-q", "-b", branch)
	write(t, clone, ctrlPath, "findings: round 2 — fix the aria-current\n")
	write(t, clone, "feature.txt", "the actual work\n")
	gitT(t, clone, "add", ".")
	gitT(t, clone, "commit", "-q", "-m", "T-44 work (reviewer round)")

	// Base moves on and DELETES the control file, as a later land does.
	gitT(t, clone, "checkout", "-q", base)
	gitT(t, clone, "rm", "-q", ctrlPath)
	gitT(t, clone, "commit", "-q", "-m", "land T-45 (drops the stray control file)")
	return clone, base, branch
}

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// TestSquashMerge_ControlArtifactConflict_AutoResolves proves the V-44 wedge is gone:
// a modify/delete conflict confined to .conductor/REVIEW.md is dropped, the merge lands
// the real product work, and the control file is NOT resurrected onto the base.
func TestSquashMerge_ControlArtifactConflict_AutoResolves(t *testing.T) {
	ctx := context.Background()
	clone, base, branch := setupControlArtifactConflict(t, ".conductor/REVIEW.md")

	m := NewGitMerger(func(string) string { return clone })
	sha, err := m.SquashMerge(ctx,
		statestore.Project{ID: "proj-1", BaseBranch: base},
		statestore.Task{ID: "T-44", ProjectID: "proj-1"},
		engine.Workspace{Path: clone, Branch: branch})
	if err != nil {
		t.Fatalf("SquashMerge should auto-resolve a .conductor/ conflict, got: %v", err)
	}
	if sha == "" {
		t.Fatalf("empty merge sha")
	}
	// The product work landed.
	if got := gitT(t, clone, "show", base+":feature.txt"); got != "the actual work" {
		t.Fatalf("feature.txt not merged: %q", got)
	}
	// The control file stayed deleted (never resurrected by the merge).
	files := gitT(t, clone, "ls-tree", "-r", "--name-only", base)
	if strings.Contains(files, ".conductor/REVIEW.md") {
		t.Fatalf("control artifact resurrected onto base:\n%s", files)
	}
	// The base advanced to the merge commit, and the worktree is clean.
	if head := gitT(t, clone, "rev-parse", base); head != sha {
		t.Fatalf("base %s HEAD %q != merge sha %q", base, head, sha)
	}
	if st := gitT(t, clone, "status", "--porcelain"); st != "" {
		t.Fatalf("worktree not clean after auto-resolved merge:\n%s", st)
	}
}

// TestSquashMerge_ProductConflict_StillFailsAndRestoresBase proves the escape hatch is
// NARROW: the same shape on a PRODUCT path is not silently dropped — the merge fails and
// the base is restored to its pre-merge tip with a clean worktree, exactly as before.
func TestSquashMerge_ProductConflict_StillFailsAndRestoresBase(t *testing.T) {
	ctx := context.Background()
	clone, base, branch := setupControlArtifactConflict(t, "src/app.ts")

	before := gitT(t, clone, "rev-parse", base)
	m := NewGitMerger(func(string) string { return clone })
	_, err := m.SquashMerge(ctx,
		statestore.Project{ID: "proj-1", BaseBranch: base},
		statestore.Task{ID: "T-44", ProjectID: "proj-1"},
		engine.Workspace{Path: clone, Branch: branch})
	if err == nil {
		t.Fatalf("a PRODUCT modify/delete conflict must NOT be auto-resolved")
	}
	if after := gitT(t, clone, "rev-parse", base); after != before {
		t.Fatalf("base ref advanced on a failed merge: %q -> %q", before, after)
	}
	if st := gitT(t, clone, "status", "--porcelain"); st != "" {
		t.Fatalf("base not restored clean after failed merge:\n%s", st)
	}
}

// TestSquashMerge_RecipeConfigConflict_IsProductContent guards the one .conductor/ path
// that is NOT conductor scratch: a repo's OWN recipe (ADR-0009) must never be dropped.
func TestSquashMerge_RecipeConfigConflict_IsProductContent(t *testing.T) {
	ctx := context.Background()
	clone, base, branch := setupControlArtifactConflict(t, ".conductor/config.yaml")

	m := NewGitMerger(func(string) string { return clone })
	if _, err := m.SquashMerge(ctx,
		statestore.Project{ID: "proj-1", BaseBranch: base},
		statestore.Task{ID: "T-44", ProjectID: "proj-1"},
		engine.Workspace{Path: clone, Branch: branch}); err == nil {
		t.Fatalf(".conductor/config.yaml is a repo's own recipe — it must NOT be auto-dropped")
	}
}

// TestSquashMerge_MixedConflict_NotAutoResolved proves a conflict that touches BOTH a
// control artifact and a product file is treated as a real conflict (all-or-nothing).
func TestSquashMerge_MixedConflict_NotAutoResolved(t *testing.T) {
	ctx := context.Background()
	clone, base, branch := setupControlArtifactConflict(t, ".conductor/REVIEW.md")

	// Add a product modify/delete on top of the control-file one.
	gitT(t, clone, "checkout", "-q", branch)
	write(t, clone, "product.txt", "branch edit\n")
	gitT(t, clone, "add", ".")
	gitT(t, clone, "commit", "-q", "-m", "touch product")
	gitT(t, clone, "checkout", "-q", base)
	gitT(t, clone, "rm", "-q", "product.txt")
	gitT(t, clone, "commit", "-q", "-m", "base drops product")

	before := gitT(t, clone, "rev-parse", base)
	m := NewGitMerger(func(string) string { return clone })
	if _, err := m.SquashMerge(ctx,
		statestore.Project{ID: "proj-1", BaseBranch: base},
		statestore.Task{ID: "T-44", ProjectID: "proj-1"},
		engine.Workspace{Path: clone, Branch: branch}); err == nil {
		t.Fatalf("a conflict touching product content must NOT be auto-resolved")
	}
	if after := gitT(t, clone, "rev-parse", base); after != before {
		t.Fatalf("base advanced on failed mixed merge")
	}
}
