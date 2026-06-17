package verify

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/everva/conductor-platform/internal/engine"
)

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

// seedRepoWorktree builds a local throwaway repo with one commit and returns its
// path. It stands in for the performer's develop worktree. No network.
func seedRepoWorktree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "develop")
	git(t, dir, "config", "user.name", "test")
	git(t, dir, "config", "user.email", "test@test")
	if err := os.WriteFile(filepath.Join(dir, "code.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "work [task:T-1]")
	return dir
}

// fakeHoldoutStore is an injected, repo-external holdout source for tests. It is
// the swappable interface seam (Faz-1b: Postgres/private repo). It yields a
// single holdout file whose content decides pass/fail.
type fakeHoldoutStore struct {
	name    string
	relPath string
	content []byte
}

func (f fakeHoldoutStore) Fetch(_ context.Context, _ string) (Holdout, error) {
	return Holdout{Name: f.name, Files: map[string][]byte{f.relPath: f.content}}, nil
}

// passingGate / failingGate are deterministic recipe gate commands.
func passingGate(name string) Gate { return Gate{Name: name, Argv: []string{"true"}} }
func failingGate(name string) Gate { return Gate{Name: name, Argv: []string{"false"}} }

// passingHoldoutScript runs the holdout file as a shell script that exits 0.
// The holdout's "result" is its script exit code, so it is fully deterministic.
func holdoutCmd(rel string) []string { return []string{"sh", rel} }

func TestVerifier_Gates_ProduceChecks(t *testing.T) {
	ctx := context.Background()
	ws := engine.Workspace{Path: seedRepoWorktree(t), Branch: "develop"}
	store := fakeHoldoutStore{name: "h1", relPath: "h_test.sh", content: []byte("exit 0\n")}
	v := New(store, Config{HoldoutCmd: holdoutCmd("h_test.sh")})

	gates := []Gate{passingGate("build"), passingGate("test"), passingGate("vet"), passingGate("lint")}
	res, checks, err := v.Verify(ctx, engine.Verdict{Result: "pass"}, ws, gates, "scn-1")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(checks) != 4 {
		t.Fatalf("expected 4 checks, got %d: %+v", len(checks), checks)
	}
	for _, c := range checks {
		if c.Name == "" || c.Result != "pass" {
			t.Fatalf("bad check: %+v", c)
		}
	}
	if res.Result != "pass" {
		t.Fatalf("expected pass, got %+v", res)
	}
}

func TestVerifier_HoldoutPass_ResultPass(t *testing.T) {
	ctx := context.Background()
	ws := engine.Workspace{Path: seedRepoWorktree(t), Branch: "develop"}
	store := fakeHoldoutStore{name: "h1", relPath: "h_test.sh", content: []byte("exit 0\n")}
	v := New(store, Config{HoldoutCmd: holdoutCmd("h_test.sh")})

	res, _, err := v.Verify(ctx, engine.Verdict{Result: "pass"}, ws, []Gate{passingGate("build")}, "scn-1")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.Result != "pass" {
		t.Fatalf("gates+holdout green should be pass, got %+v", res)
	}
}

func TestVerifier_HoldoutFail_ChangesRequested(t *testing.T) {
	ctx := context.Background()
	ws := engine.Workspace{Path: seedRepoWorktree(t), Branch: "develop"}
	// Holdout-breaking commit: holdout script exits non-zero.
	store := fakeHoldoutStore{name: "secret-holdout", relPath: "h_test.sh", content: []byte("exit 1\n")}
	v := New(store, Config{HoldoutCmd: holdoutCmd("h_test.sh")})

	res, _, err := v.Verify(ctx, engine.Verdict{Result: "pass"}, ws, []Gate{passingGate("build")}, "scn-1")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.Result != "changes-requested" {
		t.Fatalf("holdout fail must be changes-requested, got %+v", res)
	}
	if len(res.Findings) == 0 {
		t.Fatalf("expected concrete findings for failing holdout")
	}
	// Secrecy: holdout file contents must not leak into findings.
	for _, f := range res.Findings {
		if strings.Contains(f, "exit 1") {
			t.Fatalf("holdout contents leaked into findings: %q", f)
		}
	}
}

func TestVerifier_IgnoresSelfReportedVerdict(t *testing.T) {
	ctx := context.Background()
	ws := engine.Workspace{Path: seedRepoWorktree(t), Branch: "develop"}
	store := fakeHoldoutStore{name: "h1", relPath: "h_test.sh", content: []byte("exit 0\n")}
	v := New(store, Config{HoldoutCmd: holdoutCmd("h_test.sh")})

	// Performer self-reports pass, but a real gate fails -> changes-requested.
	res, _, err := v.Verify(ctx, engine.Verdict{Result: "pass"}, ws, []Gate{failingGate("test")}, "scn-1")
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.Result != "changes-requested" {
		t.Fatalf("failing gate must override self-reported pass, got %+v", res)
	}
}

func TestVerifier_VerifyWorktreeCleanedUp(t *testing.T) {
	ctx := context.Background()
	devPath := seedRepoWorktree(t)
	ws := engine.Workspace{Path: devPath, Branch: "develop"}
	store := fakeHoldoutStore{name: "h1", relPath: "h_test.sh", content: []byte("exit 1\n")}

	var captured string
	v := New(store, Config{
		HoldoutCmd:       holdoutCmd("h_test.sh"),
		onVerifyWorktree: func(p string) { captured = p },
	})

	if _, _, err := v.Verify(ctx, engine.Verdict{Result: "pass"}, ws, []Gate{passingGate("build")}, "scn-1"); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if captured == "" {
		t.Fatalf("expected a verify-worktree to be created")
	}
	if _, err := os.Stat(captured); !os.IsNotExist(err) {
		t.Fatalf("verify-worktree not cleaned up (even on holdout fail): %v", err)
	}
	// Develop worktree must be untouched: no holdout file copied in.
	if _, err := os.Stat(filepath.Join(devPath, "h_test.sh")); !os.IsNotExist(err) {
		t.Fatalf("holdout leaked into develop worktree")
	}
}

func TestVerifier_DevelopWorktree_Unmutated(t *testing.T) {
	ctx := context.Background()
	devPath := seedRepoWorktree(t)
	ws := engine.Workspace{Path: devPath, Branch: "develop"}
	before := git(t, devPath, "rev-parse", "HEAD")
	beforeStatus := git(t, devPath, "status", "--porcelain")

	store := fakeHoldoutStore{name: "h1", relPath: "h_test.sh", content: []byte("exit 0\n")}
	v := New(store, Config{HoldoutCmd: holdoutCmd("h_test.sh")})
	if _, _, err := v.Verify(ctx, engine.Verdict{Result: "pass"}, ws, []Gate{passingGate("build")}, "scn-1"); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	after := git(t, devPath, "rev-parse", "HEAD")
	afterStatus := git(t, devPath, "status", "--porcelain")
	if before != after || beforeStatus != afterStatus {
		t.Fatalf("develop worktree mutated by verify: head %s->%s status %q->%q", before, after, beforeStatus, afterStatus)
	}
}
