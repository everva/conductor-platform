package provisioner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/everva/conductor-platform/internal/statestore"
)

// TestFetchBase_FastForwards_WhenLocalBehind is the ordinary path: origin advanced, the local
// base ref is an ancestor of it, so the ref moves forward and worktrees cut from it are current.
func TestFetchBase_FastForwards_WhenLocalBehind(t *testing.T) {
	ctx := context.Background()
	remote := seedRemote(t, "develop")
	p := newProvisioner(t)
	proj := statestore.Project{ID: "proj1", Repo: remote, BaseBranch: "develop"}

	if err := p.EnsureClone(ctx, proj); err != nil {
		t.Fatalf("EnsureClone: %v", err)
	}
	clone := p.ClonePath("proj1")

	// Advance ORIGIN by one commit (as another author would).
	work := t.TempDir()
	git(t, work, "clone", "-q", remote, work+"/c")
	wc := work + "/c"
	git(t, wc, "config", "user.name", "test")
	git(t, wc, "config", "user.email", "test@test")
	// The bare origin's HEAD is its init default, not develop, so the clone checks nothing out.
	git(t, wc, "checkout", "-q", "-B", "develop", "refs/remotes/origin/develop")
	if err := os.WriteFile(filepath.Join(wc, "new.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	git(t, wc, "add", ".")
	git(t, wc, "commit", "-q", "-m", "origin advances")
	git(t, wc, "push", "-q", "origin", "develop")
	want := git(t, wc, "rev-parse", "HEAD")

	if err := p.fetchBase(ctx, clone, "develop"); err != nil {
		t.Fatalf("fetchBase should fast-forward: %v", err)
	}
	if got := git(t, clone, "rev-parse", "refs/heads/develop"); got != want {
		t.Fatalf("local base = %q, want origin tip %q", got, want)
	}
}

// TestFetchBase_RefusesToDiscardLocalCommits is the phantom-land fence.
//
// An agent with -no-push squash-merges into its LOCAL base and reports the merge SHA, so the
// gateway marks the task done. The old fetchBase then force-moved the ref back to origin's tip
// on the next provision, orphaning the land commit — two ecommerce-backend endpoints were "done"
// with no code on the base branch and no error anywhere. fetchBase must now REFUSE and say why.
func TestFetchBase_RefusesToDiscardLocalCommits(t *testing.T) {
	ctx := context.Background()
	remote := seedRemote(t, "develop")
	p := newProvisioner(t)
	proj := statestore.Project{ID: "proj1", Repo: remote, BaseBranch: "develop"}

	if err := p.EnsureClone(ctx, proj); err != nil {
		t.Fatalf("EnsureClone: %v", err)
	}
	clone := p.ClonePath("proj1")

	// The un-pushed local land commit.
	git(t, clone, "config", "user.name", "test")
	git(t, clone, "config", "user.email", "test@test")
	git(t, clone, "checkout", "-q", "develop")
	if err := os.WriteFile(filepath.Join(clone, "route.ts"), []byte("endpoint\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	git(t, clone, "add", ".")
	git(t, clone, "commit", "-q", "-m", "conductor: land B-admin-brand-merges-export")
	landSHA := git(t, clone, "rev-parse", "HEAD")

	err := p.fetchBase(ctx, clone, "develop")
	if err == nil {
		t.Fatalf("fetchBase must REFUSE to discard an un-pushed local land commit")
	}
	if !strings.Contains(err.Error(), "refusing to fast-forward") || !strings.Contains(err.Error(), "no-push") {
		t.Fatalf("the error must name the cause so an operator can act, got: %v", err)
	}
	// The commit is still there — nothing was silently dropped.
	if got := git(t, clone, "rev-parse", "refs/heads/develop"); got != landSHA {
		t.Fatalf("local base moved despite the refusal: %q != %q", got, landSHA)
	}
	if body := git(t, clone, "log", "-1", "--format=%s", "develop"); !strings.Contains(body, "conductor: land") {
		t.Fatalf("the land commit was lost: %q", body)
	}
}

// TestFetchBase_CreatesMissingLocalRef covers a clone whose default branch is not the base:
// there is no local ref to protect, so it is simply created at the fetched tip.
func TestFetchBase_CreatesMissingLocalRef(t *testing.T) {
	ctx := context.Background()
	remote := seedRemote(t, "develop")
	p := newProvisioner(t)
	proj := statestore.Project{ID: "proj1", Repo: remote, BaseBranch: "develop"}

	if err := p.EnsureClone(ctx, proj); err != nil {
		t.Fatalf("EnsureClone: %v", err)
	}
	clone := p.ClonePath("proj1")

	// EnsureClone leaves HEAD on the bare origin's (unborn) default branch, so the base ref is
	// not checked out and can simply be deleted — the state a clone is in before its first fetch.
	git(t, clone, "update-ref", "-d", "refs/heads/develop")

	if err := p.fetchBase(ctx, clone, "develop"); err != nil {
		t.Fatalf("fetchBase should create a missing local base ref: %v", err)
	}
	want := git(t, clone, "rev-parse", "refs/remotes/origin/develop")
	if got := git(t, clone, "rev-parse", "refs/heads/develop"); got != want {
		t.Fatalf("created ref = %q, want %q", got, want)
	}
}
