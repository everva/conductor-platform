package conductor

import (
	"context"
	"errors"
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
//
// Remote-push (ADR-0022, opt-in, default OFF): after a SUCCESSFUL squash-merge +
// commit, when push is enabled, the merger pushes the advanced base branch to a
// remote (default `origin`), authenticated via a gh-token credential helper
// (ADR-0017; deploy-keys forbidden). The local merge is the source of truth: a
// push failure NEVER undoes the merge and NEVER fails the task — it is surfaced as
// a PushError carrying the valid local merge SHA so the tick can log it and emit a
// push-failed event (merge landed locally, remote not updated). Default-OFF keeps
// behavior byte-identical to the local-only Faz-1 model.
type GitMerger struct {
	// clonePath resolves the owning clone directory for a project. It is injected
	// so the merger does not duplicate the provisioner's layout knowledge; a nil
	// value falls back to deriving the clone from the worktree's git common dir.
	clonePath func(projectID string) string
	// push is the opt-in remote-push config (ADR-0022). The zero value (Enabled
	// false) means local-only merge — the pre-1.5-c behavior — so a GitMerger
	// constructed without WithPush never touches a remote.
	push PushConfig
}

// PushConfig is the opt-in remote-push configuration for GitMerger (ADR-0022). The
// zero value is push DISABLED (local-only merge, the default/backward-compatible
// behavior). Construct via WithPush; the token is delivered to git via a credential
// helper, never via argv/env, and is NEVER logged or committed.
type PushConfig struct {
	// Enabled turns the post-merge remote-push step ON. Default false = local-only
	// merge (no remote contact), byte-identical to the pre-1.5-c behavior.
	Enabled bool
	// Remote is the git remote pushed to on a successful merge. Empty defaults to
	// defaultPushRemote ("origin").
	Remote string
	// GHToken is the gh-token the push credential helper authenticates with
	// (ADR-0017). Empty means no helper is installed (e.g. a local bare-repo origin
	// that needs no auth, as in tests). It is never logged or committed; push errors
	// redact it.
	GHToken string
}

// defaultPushRemote is the remote GitMerger pushes the merged base to when a
// PushConfig enables push without naming a remote (ADR-0022).
const defaultPushRemote = "origin"

// PushError signals that the squash-merge SUCCEEDED locally but the subsequent
// opt-in remote-push FAILED (ADR-0022). It is the load-bearing signal shape: when
// SquashMerge returns a non-nil error that is a *PushError, the SHA it returns is
// the VALID local merge commit (the task is done, merged locally) and Err is the
// push failure — the remote was NOT updated. The tick keys on this via errors.As to
// keep the task done while surfacing a push-failed event/log (never silent-loss,
// never fake-green, never undo the local merge). Any OTHER error from SquashMerge
// is a genuine merge failure (empty SHA, base not advanced).
type PushError struct {
	// SHA is the valid local merge commit; the base branch advanced to it.
	SHA string
	// Remote is the remote the push targeted.
	Remote string
	// Err is the underlying (token-redacted) push failure.
	Err error
}

// Error renders the push failure, making clear the merge landed locally.
func (e *PushError) Error() string {
	return fmt.Sprintf("git merger: merged locally (sha %s) but push to %q FAILED (remote not updated): %v", e.SHA, e.Remote, e.Err)
}

// Unwrap exposes the underlying push error for errors.Is/As chains.
func (e *PushError) Unwrap() error { return e.Err }

// Compile-time assertion that *GitMerger satisfies the Merger seam.
var _ Merger = (*GitMerger)(nil)

// Option configures a GitMerger at construction (additive; ADR-0021). Options are
// applied in order after the base merger is built.
type Option func(*GitMerger)

// WithPush enables the opt-in post-merge remote-push (ADR-0022). With cfg.Enabled
// false it is a no-op (local-only merge), so passing a disabled config is identical
// to not passing the option at all. An empty cfg.Remote defaults to "origin".
func WithPush(cfg PushConfig) Option {
	return func(m *GitMerger) {
		if cfg.Remote == "" {
			cfg.Remote = defaultPushRemote
		}
		m.push = cfg
	}
}

// NewGitMerger returns a GitMerger that resolves a project's clone via clonePath.
// Pass the provisioner's clone-path function so the merger merges in the same
// clone the worktree was cut from. A nil clonePath derives the clone from the
// worktree itself. Options (e.g. WithPush) are additive; with none the merger is
// local-only (the pre-1.5-c behavior).
func NewGitMerger(clonePath func(projectID string) string, opts ...Option) *GitMerger {
	m := &GitMerger{clonePath: clonePath}
	for _, opt := range opts {
		opt(m)
	}
	return m
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

	// Record the base tip BEFORE touching the working tree. On ANY failure after
	// this checkout (a squash conflict, a failed commit, ...) the base checkout must
	// be restored to THIS exact commit with a clean worktree+index so the next tick
	// re-derives from pristine state — a dirty/conflicted base corrupts the loop. The
	// base ref must NOT advance on failure.
	baseRef, err := gitOut(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git merger: resolve base ref %q: %w", project.BaseBranch, err)
	}
	baseRef = strings.TrimSpace(baseRef)

	if err := git(ctx, repo, "merge", "--squash", ws.Branch); err != nil {
		m.restoreBase(ctx, repo, baseRef)
		return "", fmt.Errorf("git merger: squash %q: %w", ws.Branch, err)
	}
	msg := fmt.Sprintf("%s\n\n[task:%s]", commitSubject(task), task.ID)
	if err := git(ctx, repo, "commit", "--allow-empty", "-m", msg); err != nil {
		m.restoreBase(ctx, repo, baseRef)
		return "", fmt.Errorf("git merger: commit squash for %q: %w", task.ID, err)
	}
	sha, err := gitOut(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		// The commit succeeded but we cannot read it back: the base was advanced, so
		// do NOT restore (that would discard a real merge); surface the read error.
		return "", fmt.Errorf("git merger: resolve merge sha: %w", err)
	}
	mergeSHA := strings.TrimSpace(sha)

	// Opt-in remote-push (ADR-0022), AFTER a successful merge+commit so it never
	// interacts with restoreBase (that runs only on FAILURE). The local merge is
	// already the source of truth: a push failure does NOT undo the merge and does
	// NOT fail the task — it is returned as a *PushError carrying the valid merge
	// SHA so the tick keeps the task done while surfacing a push-failed event/log.
	// Default-OFF (push disabled) is a no-op, so behavior is byte-identical to the
	// local-only model.
	if m.push.Enabled {
		if perr := m.pushBase(ctx, repo, project.BaseBranch); perr != nil {
			return mergeSHA, &PushError{SHA: mergeSHA, Remote: m.push.Remote, Err: perr}
		}
	}
	return mergeSHA, nil
}

// pushBase pushes the (already-advanced) base branch to the configured remote
// (ADR-0022), authenticated via a gh-token credential helper installed on the
// clone's LOCAL config (ADR-0017; deploy-keys forbidden), reusing the provisioner/
// holdout pattern. The token is delivered only through the helper — never via argv
// or env, so it cannot leak into process listings — and is redacted from any
// returned error. With no token the helper is skipped (a local bare-repo origin
// needs no auth, as in tests). The caller wraps a non-nil result in a *PushError
// (the merge already stands locally); pushBase itself never undoes the merge.
func (m *GitMerger) pushBase(ctx context.Context, repo, baseBranch string) error {
	if strings.TrimSpace(m.push.GHToken) != "" {
		helper := fmt.Sprintf("!f() { echo \"username=x-access-token\"; echo \"password=%s\"; }; f", m.push.GHToken)
		if err := git(ctx, repo, "config", "--local", "credential.helper", helper); err != nil {
			return m.redactPush(fmt.Errorf("configure gh-token credential helper: %w", err))
		}
	}
	if err := git(ctx, repo, "push", m.push.Remote, baseBranch); err != nil {
		return m.redactPush(fmt.Errorf("push %s %s: %w", m.push.Remote, baseBranch, err))
	}
	return nil
}

// redactPush removes the gh-token from a push error so a git error that echoed the
// token (e.g. in a URL or the configured helper) never surfaces or is logged
// (ADR-0017/0022). With no token it returns the error unchanged.
func (m *GitMerger) redactPush(err error) error {
	if err == nil {
		return nil
	}
	tok := strings.TrimSpace(m.push.GHToken)
	if tok == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), tok, "x-access-token:REDACTED"))
}

// restoreBase returns the base checkout to baseRef with a CLEAN worktree+index
// after a failed merge, so the base branch is back at its original commit and the
// next tick starts from pristine state. It is best-effort and idempotent: it
// aborts an in-progress merge if one exists (a real --merge leaves MERGE_HEAD; a
// --squash conflict does not, so the abort is a harmless no-op there), hard-resets
// the index+tracked files to baseRef, and removes untracked residue the squash
// left behind. Errors are non-fatal — they are wrapped onto the returned error's
// context by the caller's primary failure, and there is nothing safer to do than
// best-effort cleanup. It NEVER advances the base ref.
func (m *GitMerger) restoreBase(ctx context.Context, repo, baseRef string) {
	// `git merge --abort` only succeeds when a merge is in progress (MERGE_HEAD).
	// A squash conflict leaves no MERGE_HEAD, so this errors harmlessly; the
	// subsequent hard reset is what actually clears a squash conflict's index/worktree.
	_ = git(ctx, repo, "merge", "--abort")
	// Restore tracked files + index to the recorded base tip (clears conflict
	// markers and un-advances the ref to exactly baseRef).
	_ = git(ctx, repo, "reset", "--hard", baseRef)
	// Drop any untracked files the squash introduced (new files from the task
	// branch become untracked after the reset). -d removes empty dirs too; we do
	// NOT pass -x so a project's ignored build artifacts are left untouched.
	_ = git(ctx, repo, "clean", "-fd")
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
