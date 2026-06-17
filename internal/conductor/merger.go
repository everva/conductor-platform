package conductor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/statestore"
)

// GitMerger is the real Merger: it squash-merges the verified per-task branch
// into the project's base branch in the owning clone, recording an EXACT
// `[task:<id>]` trailer (ADR-0004) that the reconciler (B-3) matches to derive
// merged->done from git. It shells out to git directly (no LLM) and is injected so
// the conductor stays testable with a fake.
//
// It operates in the per-project CLONE (the worktree's main repo), not the
// throwaway per-task worktree, because the base branch is checked out there and a
// `git worktree` cannot have the same branch checked out twice.
type GitMerger struct {
	// clonePath resolves the owning clone directory for a project. It is injected
	// so the merger does not duplicate the provisioner's layout knowledge; a nil
	// value falls back to deriving the clone from the worktree's git common dir.
	clonePath func(projectID string) string
}

// Compile-time assertion that *GitMerger satisfies the Merger seam.
var _ Merger = (*GitMerger)(nil)

// NewGitMerger returns a GitMerger that resolves a project's clone via clonePath.
// Pass the provisioner's clone-path function so the merger merges in the same
// clone the worktree was cut from. A nil clonePath derives the clone from the
// worktree itself.
func NewGitMerger(clonePath func(projectID string) string) *GitMerger {
	return &GitMerger{clonePath: clonePath}
}

// SquashMerge squash-merges ws.Branch into project.BaseBranch in the owning clone
// and commits with a `[task:<id>]` trailer, returning the new base commit SHA. It
// is the merge mechanic only; the DECISION to merge is the conductor's (gated on
// the independent verify pass, Rule#9).
func (m *GitMerger) SquashMerge(ctx context.Context, project statestore.Project, task statestore.Task, ws engine.Workspace) (string, error) {
	if project.BaseBranch == "" {
		return "", fmt.Errorf("git merger: project %q has no base branch", project.ID)
	}
	if ws.Branch == "" {
		return "", fmt.Errorf("git merger: empty branch for task %q", task.ID)
	}

	repo, err := m.repoFor(ctx, project, ws)
	if err != nil {
		return "", err
	}

	// Move onto the base branch, squash-apply the task branch, then commit with the
	// frozen trailer the reconciler keys on. A squash leaves a single base commit.
	if err := git(ctx, repo, "checkout", project.BaseBranch); err != nil {
		return "", fmt.Errorf("git merger: checkout base %q: %w", project.BaseBranch, err)
	}
	if err := git(ctx, repo, "merge", "--squash", ws.Branch); err != nil {
		return "", fmt.Errorf("git merger: squash %q: %w", ws.Branch, err)
	}
	msg := fmt.Sprintf("%s\n\n[task:%s]", commitSubject(task), task.ID)
	if err := git(ctx, repo, "commit", "--allow-empty", "-m", msg); err != nil {
		return "", fmt.Errorf("git merger: commit squash for %q: %w", task.ID, err)
	}
	sha, err := gitOut(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git merger: resolve merge sha: %w", err)
	}
	return strings.TrimSpace(sha), nil
}

// repoFor resolves the repository the merge runs in: the injected clone path when
// available, else the worktree's git common dir (its owning repo).
func (m *GitMerger) repoFor(ctx context.Context, project statestore.Project, ws engine.Workspace) (string, error) {
	if m.clonePath != nil {
		return m.clonePath(project.ID), nil
	}
	common, err := gitOut(ctx, ws.Path, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("git merger: locate owning repo: %w", err)
	}
	// --git-common-dir yields .../clone/.git; the repo worktree is its parent.
	dir := strings.TrimSpace(common)
	dir = strings.TrimSuffix(dir, "/.git")
	dir = strings.TrimSuffix(dir, "/.git/")
	return dir, nil
}

// commitSubject is the squash commit subject. It is short and factual; the
// machine-readable truth is the trailer, not the prose.
func commitSubject(task statestore.Task) string {
	return fmt.Sprintf("conductor: land %s", task.ID)
}

// git runs a git subcommand in dir with a deterministic identity so commits are
// reproducible in tests and CI, wrapping failures with the combined output.
func git(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %v: %w: %s", args, err, out)
	}
	return nil
}

// gitOut runs a git subcommand and returns its stdout, wrapping failures.
func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %v: %w", args, err)
	}
	return string(out), nil
}

// gitEnv supplies a committer identity so the squash commit never blocks on
// missing git config, plus a non-interactive terminal so auth never prompts.
func gitEnv() []string {
	return append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=conductor", "GIT_AUTHOR_EMAIL=conductor@local",
		"GIT_COMMITTER_NAME=conductor", "GIT_COMMITTER_EMAIL=conductor@local",
	)
}
