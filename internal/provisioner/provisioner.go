// Package provisioner implements the workspace leg of Faz-1a (ADR-0017): a
// single ISOLATED per-project clone plus a per-task `git worktree` cut FRESH
// from the project's base branch on a short-lived per-task branch. It produces
// the frozen engine.Workspace{Path, Branch} the EngineAdapter consumes (PRE-0)
// and never mutates that struct.
//
// Auth is a gh-token credential helper so pushes are WRITABLE; the read-only
// deploy-key trap is forbidden (ADR-0017). The token is injected via Config,
// never hard-coded. Provisioning writes only repo content + the fresh branch:
// the develop worktree NEVER receives the hidden holdout — holdout injection is
// verify's job (B-2, ADR-0018).
//
// There is no global singleton: the clone root is injected and a Provisioner is
// created with New and passed explicitly.
package provisioner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/statestore"
)

// Config configures a Provisioner. RootDir is the injected directory under which
// per-project clones and their worktrees live; GHToken is the gh credential
// token used for writable HTTPS auth (ADR-0017). Both come from the caller's
// config/env and are never hard-coded.
type Config struct {
	// RootDir is the base directory holding per-project clones (injected, no
	// global singleton).
	RootDir string
	// GHToken is the gh-token used by the credential helper for writable pushes.
	// Empty disables credential-helper installation (e.g. local-only tests that
	// never push); deploy-keys are never used (ADR-0017).
	GHToken string
	// RetainBlocked, when true, keeps a blocked task's worktree for diagnosis
	// instead of removing it (ADR-0017 retention hook). Default false = remove.
	RetainBlocked bool
}

// Provisioner ensures per-project clones and cuts per-task worktrees. It holds
// only the injected config; all state lives on disk under RootDir.
type Provisioner struct {
	cfg Config
}

// New returns a Provisioner rooted at cfg.RootDir, creating the root if needed.
// A blank RootDir is an error: the clone root must be injected explicitly.
func New(cfg Config) (*Provisioner, error) {
	if cfg.RootDir == "" {
		return nil, errors.New("provisioner: RootDir is required")
	}
	if err := os.MkdirAll(cfg.RootDir, 0o755); err != nil {
		return nil, fmt.Errorf("provisioner: create root %q: %w", cfg.RootDir, err)
	}
	return &Provisioner{cfg: cfg}, nil
}

// clonePath is the absolute path of the per-project clone for projectID.
func (p *Provisioner) clonePath(projectID string) string {
	return filepath.Join(p.cfg.RootDir, "clones", projectID)
}

// worktreePath is the absolute path of the per-task worktree for a project/task.
func (p *Provisioner) worktreePath(projectID, taskID string) string {
	return filepath.Join(p.cfg.RootDir, "worktrees", projectID, taskID)
}

// branchName is the short-lived per-task branch (ADR-0004/0017). The project + task are
// joined with a DASH (not a slash) so the branch never D/F-conflicts with a base branch that
// is itself `conductor/<projectID>`: git stores refs as files, so a `conductor/optiway` base
// (a file at refs/heads/conductor/optiway) would block creating `conductor/optiway/<task>` (a
// dir). `conductor/optiway-<task>` is a sibling file → no conflict. (Real-world bug: optiway's
// base branch is `conductor/optiway`.)
func branchName(projectID, taskID string) string {
	return fmt.Sprintf("conductor/%s-%s", projectID, taskID)
}

// EnsureClone makes sure a single isolated clone of the project exists under
// RootDir, creating it once and configuring writable gh-token auth. It is
// idempotent: a second call reuses the existing clone (it only refreshes the
// base branch), so the clone is never blown away mid-flight.
func (p *Provisioner) EnsureClone(ctx context.Context, project statestore.Project) error {
	if project.ID == "" || project.Repo == "" {
		return fmt.Errorf("provisioner: ensure clone: %w", errors.New("project ID and Repo are required"))
	}
	clone := p.clonePath(project.ID)

	if isGitRepo(clone) {
		// Reuse the existing isolated clone; just keep its auth + base fresh.
		if err := p.configureAuth(ctx, clone); err != nil {
			return err
		}
		return p.fetchBase(ctx, clone, project.BaseBranch)
	}

	if err := os.MkdirAll(filepath.Dir(clone), 0o755); err != nil {
		return fmt.Errorf("provisioner: prepare clone dir: %w", err)
	}
	// The literal "--" ends git option parsing so project.Repo is ALWAYS a
	// positional repository, never an option, even if it begins with "-" (defense in
	// depth against `git clone --upload-pack=<cmd>`-style injection; onboard also
	// validates via gitsafe). git clone accepts: clone [<options>] [--] <repo> <dir>.
	if err := runGit(ctx, p.cfg.RootDir, p.gitEnv(), "clone", "--origin", "origin", "--", project.Repo, clone); err != nil {
		return fmt.Errorf("provisioner: clone %q: %w", project.Repo, err)
	}
	if err := p.configureAuth(ctx, clone); err != nil {
		return err
	}
	return p.fetchBase(ctx, clone, project.BaseBranch)
}

// configureAuth installs a gh-token credential helper on the clone so HTTPS
// pushes are writable (ADR-0017). It NEVER installs an ssh deploy-key. With no
// token configured it is a no-op (e.g. local-only test repos).
func (p *Provisioner) configureAuth(ctx context.Context, clone string) error {
	if p.cfg.GHToken == "" {
		return nil
	}
	// A store-less inline credential helper that echoes the injected gh-token for
	// any host. This is the gh-token path (equivalent to `gh auth setup-git`),
	// not a deploy-key, and the token is never written into tracked repo content.
	helper := fmt.Sprintf("!f() { echo \"username=x-access-token\"; echo \"password=%s\"; }; f", p.cfg.GHToken)
	if err := runGit(ctx, clone, p.gitEnv(), "config", "--local", "credential.helper", helper); err != nil {
		return fmt.Errorf("provisioner: configure gh-token credential helper: %w", err)
	}
	return nil
}

// fetchBase updates the base branch from origin so worktrees are always cut from
// a current tip — no long-lived/stale base (ADR-0004). It tolerates a clone that
// already has the base checked out.
func (p *Provisioner) fetchBase(ctx context.Context, clone, base string) error {
	if base == "" {
		return fmt.Errorf("provisioner: fetch base: %w", errors.New("BaseBranch is required"))
	}
	if err := runGit(ctx, clone, p.gitEnv(), "fetch", "--prune", "origin", base); err != nil {
		return fmt.Errorf("provisioner: fetch base %q: %w", base, err)
	}
	// Fast-forward the local base ref to the freshly fetched origin tip so a
	// worktree cut from <base> is genuinely current rather than a cached tip.
	if err := runGit(ctx, clone, p.gitEnv(), "update-ref", "refs/heads/"+base, "refs/remotes/origin/"+base); err != nil {
		return fmt.Errorf("provisioner: update base ref %q: %w", base, err)
	}
	return nil
}

// Workspace ensures the project clone exists and adds a per-task git worktree on
// a fresh per-task branch cut from the current base tip, returning the frozen
// engine.Workspace{Path, Branch} (ADR-0017). It is idempotent: if a worktree
// already exists for the task it is recreated from the fresh base so the branch
// is never stale (reuse-or-recreate).
func (p *Provisioner) Workspace(ctx context.Context, project statestore.Project, task statestore.Task) (engine.Workspace, error) {
	if task.ID == "" {
		return engine.Workspace{}, fmt.Errorf("provisioner: workspace: %w", errors.New("task ID is required"))
	}
	if err := p.EnsureClone(ctx, project); err != nil {
		return engine.Workspace{}, err
	}

	clone := p.clonePath(project.ID)
	wt := p.worktreePath(project.ID, task.ID)
	branch := branchName(project.ID, task.ID)

	// Recreate-from-fresh-base: drop any prior worktree+branch for this task so a
	// re-provision always yields a current cut rather than a stale leftover.
	if err := p.removeWorktree(ctx, clone, wt); err != nil {
		return engine.Workspace{}, err
	}
	_ = runGit(ctx, clone, p.gitEnv(), "branch", "-D", branch) // best-effort; ok if absent.

	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		return engine.Workspace{}, fmt.Errorf("provisioner: prepare worktree dir: %w", err)
	}
	if err := runGit(ctx, clone, p.gitEnv(), "worktree", "add", "-b", branch, wt, project.BaseBranch); err != nil {
		return engine.Workspace{}, fmt.Errorf("provisioner: add worktree for task %q: %w", task.ID, err)
	}

	abs, err := filepath.Abs(wt)
	if err != nil {
		return engine.Workspace{}, fmt.Errorf("provisioner: resolve worktree path: %w", err)
	}
	return engine.Workspace{Path: abs, Branch: branch}, nil
}

// WorkspaceForBranch RE-ATTACHES a worktree to an EXISTING per-task branch rather
// than cutting a fresh one from the base (Faz-1.5-b, governance human-hold approve
// flow). It exists for the ONE case where the verified work must NOT be discarded:
// a T3/T4 task whose develop+verify already PASSED was HELD for a human, its
// per-task branch left intact in the clone (Cleanup removes only the worktree, never
// the branch ref). On operator APPROVAL the conductor needs that same VERIFIED
// commit back in a worktree to merge it — so this checks the branch out as-is,
// WITHOUT the `git branch -D` + re-cut-from-base that Workspace does (that would
// roll the verified work back to a bare base, defeating the approval).
//
// It is additive on the provisioner (no frozen signature changed): the normal
// develop path still uses Workspace. The named branch MUST already exist in the
// clone (the preserved verified branch); a missing branch is a clear error rather
// than a silent fresh cut, so an approval can never accidentally merge empty work.
// It is idempotent: any stale worktree for the task is removed first, then the
// existing branch is re-attached at its recorded tip.
func (p *Provisioner) WorkspaceForBranch(ctx context.Context, project statestore.Project, task statestore.Task, branch string) (engine.Workspace, error) {
	if task.ID == "" {
		return engine.Workspace{}, fmt.Errorf("provisioner: workspace-for-branch: %w", errors.New("task ID is required"))
	}
	if branch == "" {
		return engine.Workspace{}, fmt.Errorf("provisioner: workspace-for-branch: %w", errors.New("branch is required"))
	}
	// The clone must exist (the held task was developed in it, so it does); ensure
	// it and refresh auth/base, but do NOT delete the per-task branch.
	if err := p.EnsureClone(ctx, project); err != nil {
		return engine.Workspace{}, err
	}

	clone := p.clonePath(project.ID)
	wt := p.worktreePath(project.ID, task.ID)

	// The verified branch MUST already exist — never fall back to a fresh cut.
	if err := runGit(ctx, clone, p.gitEnv(), "rev-parse", "--verify", "refs/heads/"+branch); err != nil {
		return engine.Workspace{}, fmt.Errorf("provisioner: workspace-for-branch: preserved branch %q not found in clone: %w", branch, err)
	}

	// Drop only a stale worktree for the task (not the branch), then re-attach the
	// EXISTING branch — `worktree add <path> <branch>` (no -b) checks out the branch
	// as-is at its recorded tip, preserving the verified commit.
	if err := p.removeWorktree(ctx, clone, wt); err != nil {
		return engine.Workspace{}, err
	}
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		return engine.Workspace{}, fmt.Errorf("provisioner: prepare worktree dir: %w", err)
	}
	if err := runGit(ctx, clone, p.gitEnv(), "worktree", "add", wt, branch); err != nil {
		return engine.Workspace{}, fmt.Errorf("provisioner: re-attach worktree for task %q on branch %q: %w", task.ID, branch, err)
	}

	abs, err := filepath.Abs(wt)
	if err != nil {
		return engine.Workspace{}, fmt.Errorf("provisioner: resolve worktree path: %w", err)
	}
	return engine.Workspace{Path: abs, Branch: branch}, nil
}

// ErrBaseMergeConflict is returned by MergeBaseIntoWorktree when merging the base
// branch into the re-attached preserved branch CONFLICTS textually. It is a
// comparable sentinel so the conductor can distinguish a real drift-conflict (treat
// as approved-rejected/blocked, honestly) from an infrastructural git error. The
// failed merge is aborted before this is returned, leaving the worktree clean.
var ErrBaseMergeConflict = errors.New("provisioner: base merge into preserved branch conflicts")

// MergeBaseIntoWorktree merges the project's CURRENT base branch INTO the worktree's
// re-attached preserved branch (M1, the base-drift guard for the approve flow). It
// exists so the approve-merge re-verify runs against the ADVANCED base, not just the
// preserved branch tip: without this the re-verify only re-runs the gate on the OLD
// commit, so a SEMANTIC base drift (the base changed something that breaks the held
// branch's gate WITHOUT a textual conflict) would pass re-verify and only surface —
// if at all — as a textual conflict at SquashMerge. Bringing the base into the
// worktree first makes the re-verify a real drift guard.
//
// It runs `git merge --no-edit --no-ff <base>` in the worktree. On a clean merge it
// returns nil and the worktree now combines the preserved work with the drifted base
// (the caller re-verifies THIS). On a CONFLICT it `git merge --abort`s (leaving the
// worktree clean and the preserved branch tip intact) and returns ErrBaseMergeConflict
// so the caller treats it as changes-requested/blocked — honest, never fake-green. A
// non-conflict git failure is returned wrapped (infrastructural, not a drift verdict).
//
// It is additive on the provisioner (no frozen signature changed); the conductor
// reaches it via the optional BaseMerger capability (type assertion), so a provisioner
// lacking it simply skips the extra guard (the pre-M1 behavior). The base ref is kept
// current by WorkspaceForBranch's EnsureClone→fetchBase (which fast-forwards the local
// base to the origin tip) before this is called.
func (p *Provisioner) MergeBaseIntoWorktree(ctx context.Context, project statestore.Project, ws engine.Workspace) error {
	if ws.Path == "" {
		return fmt.Errorf("provisioner: merge-base: %w", errors.New("workspace path is required"))
	}
	if project.BaseBranch == "" {
		return fmt.Errorf("provisioner: merge-base: %w", errors.New("BaseBranch is required"))
	}
	// Merge the local base ref (kept current by EnsureClone→fetchBase) into the
	// worktree's checked-out preserved branch. --no-ff records a merge commit so the
	// re-verify clearly sees the combined tree; --no-edit avoids an editor prompt.
	mergeErr := runGit(ctx, ws.Path, p.gitEnv(), "merge", "--no-edit", "--no-ff", project.BaseBranch)
	if mergeErr == nil {
		return nil
	}
	// A failed merge is most likely a textual conflict. Abort it to restore a clean
	// worktree (the preserved branch tip is untouched by an aborted merge), then
	// classify: if a merge was actually in progress, it was a conflict (drift).
	inProgress := runGit(ctx, ws.Path, p.gitEnv(), "rev-parse", "--verify", "--quiet", "MERGE_HEAD") == nil
	if inProgress {
		_ = runGit(ctx, ws.Path, p.gitEnv(), "merge", "--abort")
		return fmt.Errorf("%w: base %q into branch %q: %v", ErrBaseMergeConflict, project.BaseBranch, ws.Branch, mergeErr)
	}
	// No merge in progress -> an infrastructural failure (e.g. base ref missing),
	// not a drift conflict. Surface it as a wrapped error, not the conflict sentinel.
	return fmt.Errorf("provisioner: merge-base %q into branch %q: %w", project.BaseBranch, ws.Branch, mergeErr)
}

// IsBaseMergeConflict reports whether err is the ErrBaseMergeConflict drift signal.
// It lets the conductor classify a MergeBaseIntoWorktree result as a real base-drift
// CONFLICT (→ approved-rejected/blocked) versus an infrastructural error WITHOUT
// importing the provisioner package's sentinel directly (the conductor reaches it via
// the BaseMerger capability), keeping the conductor↔provisioner coupling at the seam.
func (p *Provisioner) IsBaseMergeConflict(err error) bool {
	return errors.Is(err, ErrBaseMergeConflict)
}

// Cleanup removes the worktree and prunes it (ADR-0017). It is idempotent: a
// missing worktree is not an error. RetainBlocked is honored by the caller
// (which decides whether to call Cleanup); Cleanup itself always removes.
func (p *Provisioner) Cleanup(ctx context.Context, ws engine.Workspace) error {
	if ws.Path == "" {
		return nil
	}
	clone, err := p.cloneFromWorktree(ws.Path)
	if err != nil {
		// No owning clone resolvable: fall back to a plain directory removal so
		// cleanup is still idempotent and leaves nothing behind.
		if rmErr := os.RemoveAll(ws.Path); rmErr != nil {
			return fmt.Errorf("provisioner: cleanup remove %q: %w", ws.Path, rmErr)
		}
		return nil
	}
	if err := p.removeWorktree(ctx, clone, ws.Path); err != nil {
		return err
	}
	return nil
}

// removeWorktree removes wt from the clone and prunes stale worktree metadata.
// It is idempotent: a worktree that is already gone is not an error.
func (p *Provisioner) removeWorktree(ctx context.Context, clone, wt string) error {
	if _, err := os.Stat(wt); os.IsNotExist(err) {
		// Still prune in case metadata lingers without the directory.
		_ = runGit(ctx, clone, p.gitEnv(), "worktree", "prune")
		return nil
	}
	// --force handles a worktree with local changes (e.g. a blocked task tree).
	if err := runGit(ctx, clone, p.gitEnv(), "worktree", "remove", "--force", wt); err != nil {
		// Fall back to a directory removal + prune so cleanup never wedges.
		if rmErr := os.RemoveAll(wt); rmErr != nil {
			return fmt.Errorf("provisioner: remove worktree %q: %w", wt, errors.Join(err, rmErr))
		}
	}
	if err := runGit(ctx, clone, p.gitEnv(), "worktree", "prune"); err != nil {
		return fmt.Errorf("provisioner: prune worktrees: %w", err)
	}
	return nil
}

// cloneFromWorktree resolves the owning clone (the worktree's main repo) so
// Cleanup can prune through the right repository.
func (p *Provisioner) cloneFromWorktree(wt string) (string, error) {
	// worktreePath is RootDir/worktrees/<project>/<task>; map it back to the
	// matching clone under RootDir/clones/<project>.
	projectID := filepath.Base(filepath.Dir(wt))
	clone := p.clonePath(projectID)
	if !isGitRepo(clone) {
		return "", fmt.Errorf("provisioner: no clone for worktree %q", wt)
	}
	return clone, nil
}

// gitEnv returns the environment for git invocations. It is deterministic and
// never injects ssh/deploy-key configuration (ADR-0017); HTTPS + the gh-token
// credential helper is the only auth path.
//
// It also supplies an explicit committer identity (matching conductor.GitMerger's
// gitEnv): the approve-re-verify base-merge runs `git merge --no-ff <base>`, which
// CREATES a merge commit and therefore needs a committer. Relying on git's
// auto-detection from the OS user/hostname is non-portable — it works on a dev
// machine but FAILS with "Committer identity unknown" on a clean CI runner (GitHub-
// hosted and many self-hosted), so we pin a deterministic identity here.
func (p *Provisioner) gitEnv() []string {
	return append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0", // never block on an interactive auth prompt.
		"GIT_AUTHOR_NAME=conductor", "GIT_AUTHOR_EMAIL=conductor@local",
		"GIT_COMMITTER_NAME=conductor", "GIT_COMMITTER_EMAIL=conductor@local",
	)
}

// isGitRepo reports whether dir is an existing git repository.
func isGitRepo(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && fi.IsDir()
}

// runGit runs a git command in dir with env and returns a wrapped error
// carrying the combined output on failure.
func runGit(ctx context.Context, dir string, env []string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %v: %w: %s", args, err, out)
	}
	return nil
}
