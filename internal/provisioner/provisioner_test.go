package provisioner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/everva/conductor-platform/internal/statestore"
)

// git runs a git command in dir, failing the test on error. Used only to build
// the local throwaway seed repos the tests provision from — never a live repo.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// seedRemote builds a local bare "remote" repo with one commit on baseBranch and
// returns its path. No network, no live optiway repo (B-1 acceptance).
func seedRemote(t *testing.T, baseBranch string) string {
	t.Helper()
	root := t.TempDir()
	work := filepath.Join(root, "work")
	bare := filepath.Join(root, "remote.git")

	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatalf("mkdir work: %v", err)
	}
	git(t, work, "init", "-q", "-b", baseBranch)
	git(t, work, "config", "user.name", "test")
	git(t, work, "config", "user.email", "test@test")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	git(t, work, "add", ".")
	git(t, work, "commit", "-q", "-m", "seed")
	git(t, work, "init", "-q", "--bare", bare)
	git(t, work, "remote", "add", "origin", bare)
	git(t, work, "push", "-q", "origin", baseBranch)
	return bare
}

func newProvisioner(t *testing.T) *Provisioner {
	t.Helper()
	root := t.TempDir()
	p, err := New(Config{RootDir: root, GHToken: "ghp_faketoken"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func TestProvisioner_Workspace_BranchesFromBase(t *testing.T) {
	ctx := context.Background()
	remote := seedRemote(t, "develop")
	p := newProvisioner(t)
	proj := statestore.Project{ID: "proj1", Repo: remote, BaseBranch: "develop"}
	task := statestore.Task{ID: "T-1", ProjectID: "proj1"}

	ws, err := p.Workspace(ctx, proj, task)
	if err != nil {
		t.Fatalf("Workspace: %v", err)
	}

	// The fresh worktree branch tip must equal the base branch tip at creation.
	clone := p.clonePath(proj.ID)
	baseTip := git(t, clone, "rev-parse", "develop")
	wtTip := git(t, ws.Path, "rev-parse", "HEAD")
	if baseTip != wtTip {
		t.Fatalf("worktree not cut fresh from base: base=%s worktree=%s", baseTip, wtTip)
	}
}

func TestProvisioner_Workspace_ReturnsWorkspaceStruct(t *testing.T) {
	ctx := context.Background()
	remote := seedRemote(t, "develop")
	p := newProvisioner(t)
	proj := statestore.Project{ID: "proj1", Repo: remote, BaseBranch: "develop"}
	task := statestore.Task{ID: "T-1", ProjectID: "proj1"}

	ws, err := p.Workspace(ctx, proj, task)
	if err != nil {
		t.Fatalf("Workspace: %v", err)
	}
	if !filepath.IsAbs(ws.Path) {
		t.Fatalf("Path not absolute: %q", ws.Path)
	}
	if fi, err := os.Stat(ws.Path); err != nil || !fi.IsDir() {
		t.Fatalf("worktree path does not exist as dir: %q err=%v", ws.Path, err)
	}
	want := "conductor/proj1/T-1"
	if ws.Branch != want {
		t.Fatalf("Branch = %q, want %q", ws.Branch, want)
	}
}

func TestProvisioner_Cleanup_RemovesWorktree_Idempotent(t *testing.T) {
	ctx := context.Background()
	remote := seedRemote(t, "develop")
	p := newProvisioner(t)
	proj := statestore.Project{ID: "proj1", Repo: remote, BaseBranch: "develop"}
	task := statestore.Task{ID: "T-1", ProjectID: "proj1"}

	ws, err := p.Workspace(ctx, proj, task)
	if err != nil {
		t.Fatalf("Workspace: %v", err)
	}
	if err := p.Cleanup(ctx, ws); err != nil {
		t.Fatalf("Cleanup: %v", err)
	}
	if _, err := os.Stat(ws.Path); !os.IsNotExist(err) {
		t.Fatalf("worktree path still present after cleanup: err=%v", err)
	}
	// Second cleanup is a no-op.
	if err := p.Cleanup(ctx, ws); err != nil {
		t.Fatalf("second Cleanup not idempotent: %v", err)
	}
}

func TestProvisioner_PerProjectClone_Idempotent(t *testing.T) {
	ctx := context.Background()
	remote := seedRemote(t, "develop")
	p := newProvisioner(t)
	proj := statestore.Project{ID: "proj1", Repo: remote, BaseBranch: "develop"}

	if err := p.EnsureClone(ctx, proj); err != nil {
		t.Fatalf("EnsureClone 1: %v", err)
	}
	clone := p.clonePath(proj.ID)
	marker := filepath.Join(clone, ".git", "conductor-marker")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	// A second EnsureClone must reuse the existing clone (marker survives).
	if err := p.EnsureClone(ctx, proj); err != nil {
		t.Fatalf("EnsureClone 2: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("clone was re-created (marker gone): %v", err)
	}
}

func TestProvisioner_NoDeployKey(t *testing.T) {
	ctx := context.Background()
	remote := seedRemote(t, "develop")
	p := newProvisioner(t)
	proj := statestore.Project{ID: "proj1", Repo: remote, BaseBranch: "develop"}

	if err := p.EnsureClone(ctx, proj); err != nil {
		t.Fatalf("EnsureClone: %v", err)
	}
	clone := p.clonePath(proj.ID)

	// Auth uses a gh-token credential helper, not an ssh deploy-key: the local
	// config must carry a credential.helper and must NOT carry core.sshCommand.
	cfg := git(t, clone, "config", "--local", "--list")
	if !strings.Contains(cfg, "credential.helper") {
		t.Fatalf("expected gh-token credential helper in git config, got:\n%s", cfg)
	}
	if strings.Contains(cfg, "core.sshcommand") || strings.Contains(cfg, "sshCommand") {
		t.Fatalf("deploy-key/ssh path is forbidden, found ssh config:\n%s", cfg)
	}
}

func TestProvisioner_Workspace_Idempotent_ReuseOrRecreate(t *testing.T) {
	ctx := context.Background()
	remote := seedRemote(t, "develop")
	p := newProvisioner(t)
	proj := statestore.Project{ID: "proj1", Repo: remote, BaseBranch: "develop"}
	task := statestore.Task{ID: "T-1", ProjectID: "proj1"}

	ws1, err := p.Workspace(ctx, proj, task)
	if err != nil {
		t.Fatalf("Workspace 1: %v", err)
	}
	ws2, err := p.Workspace(ctx, proj, task)
	if err != nil {
		t.Fatalf("Workspace 2 (idempotent reuse/recreate): %v", err)
	}
	if ws1.Branch != ws2.Branch {
		t.Fatalf("branch drifted: %q vs %q", ws1.Branch, ws2.Branch)
	}
	if fi, err := os.Stat(ws2.Path); err != nil || !fi.IsDir() {
		t.Fatalf("worktree path missing after re-provision: %v", err)
	}
}

func TestProvisioner_DevelopWorktree_HasNoHoldout(t *testing.T) {
	// Isolation guarantee (ADR-0018): provisioning writes only repo content +
	// the fresh branch; the develop worktree never receives a hidden holdout.
	ctx := context.Background()
	remote := seedRemote(t, "develop")
	p := newProvisioner(t)
	proj := statestore.Project{ID: "proj1", Repo: remote, BaseBranch: "develop"}
	task := statestore.Task{ID: "T-1", ProjectID: "proj1"}

	ws, err := p.Workspace(ctx, proj, task)
	if err != nil {
		t.Fatalf("Workspace: %v", err)
	}
	entries, err := os.ReadDir(ws.Path)
	if err != nil {
		t.Fatalf("read worktree: %v", err)
	}
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if strings.Contains(name, "holdout") {
			t.Fatalf("develop worktree leaked a holdout artifact: %q", e.Name())
		}
	}
}
