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

// TestVerifier_NoHoldoutRunner_SkipsHoldout: a scenario carries a stored holdout (the distiller
// auto-generates one, Faz-S S4) but NO runner is configured. The holdout would FAIL if run (exit 1)
// — proving it is SKIPPED, not run: the result is pass purely on the public gates, with no holdout
// finding. Without this, every auto-holdout job would hard-block on the missing runner (the A-1 hit).
func TestVerifier_NoHoldoutRunner_SkipsHoldout(t *testing.T) {
	ctx := context.Background()
	ws := engine.Workspace{Path: seedRepoWorktree(t), Branch: "develop"}
	store := fakeHoldoutStore{name: "secret", relPath: "h_test.sh", content: []byte("exit 1\n")}
	v := New(store, Config{}) // HoldoutCmd empty → no runner

	res, _, err := v.Verify(ctx, engine.Verdict{Result: "pass"}, ws, []Gate{passingGate("build")}, "scn-1")
	if err != nil {
		t.Fatalf("Verify must not error when no holdout runner is configured: %v", err)
	}
	if res.Result != "pass" {
		t.Fatalf("no runner → holdout skipped → pass on public gates, got %+v", res)
	}
	if len(res.Findings) != 0 {
		t.Fatalf("a skipped holdout must contribute no findings, got %+v", res.Findings)
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

// TestRunGate_StripsConductorEnv proves the gate subprocess does NOT inherit the
// daemon's own secrets — CONDUCTOR_* config (FIX #2) AND GH_TOKEN (S-2): a
// CONDUCTOR_DSN and a GH_TOKEN set in the parent must be invisible to the gated
// project's commands, so they cannot leak into the attacker-influenced project's
// env-reading tests / exfiltrate. The gate greps its env and exits non-zero
// (fails) if either is present; a passing gate proves both were stripped. A
// control assertion confirms a NON-secret var (PATH) still passes through so we
// only strip the daemon's known secrets, not the whole environment.
func TestRunGate_StripsConductorEnv(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	t.Setenv("CONDUCTOR_DSN", "postgres://leak:leak@host/db")
	t.Setenv("GH_TOKEN", "ghp_faketokenvalue")

	dir := t.TempDir()

	// Gate fails (exit 1) iff CONDUCTOR_* or GH_TOKEN is visible in its env.
	leakGate := Gate{Name: "no-secret-env", Argv: []string{
		"sh", "-c", `if env | grep -qE '^(CONDUCTOR_|GH_TOKEN=)'; then exit 1; fi; exit 0`,
	}}
	c := runGate(context.Background(), dir, leakGate)
	if c.Result != checkPass {
		t.Fatalf("gate saw a daemon secret (CONDUCTOR_*/GH_TOKEN) in its env (env not sanitized): %+v", c)
	}

	// Control: PATH (a non-secret var the toolchain needs) MUST still reach the
	// gate, proving we strip only the daemon's secrets and not the whole env.
	pathGate := Gate{Name: "has-path", Argv: []string{
		"sh", "-c", `if [ -z "$PATH" ]; then exit 1; fi; exit 0`,
	}}
	if c := runGate(context.Background(), dir, pathGate); c.Result != checkPass {
		t.Fatalf("PATH did not reach the gate (over-sanitized env): %+v", c)
	}
}

// TestRunGate_MissingBinaryFailsDeterministically proves a configured gate whose
// binary is NOT installed (e.g. an opt-in golangci-lint on a host without it)
// FAILS deterministically rather than being silently skipped (no fake-green,
// Rule#9). The Evidence surfaces the exec error so the failure is diagnosable.
func TestRunGate_MissingBinaryFailsDeterministically(t *testing.T) {
	dir := t.TempDir()
	g := Gate{Name: "golangci-lint", Argv: []string{"definitely-not-a-real-binary-xyz", "run"}}
	c := runGate(context.Background(), dir, g)
	if c.Result != checkFail {
		t.Fatalf("missing-binary gate result = %q, want %q (must fail, never skip)", c.Result, checkFail)
	}
	if c.Evidence == "" || c.Evidence == "exit non-zero (no output)" {
		t.Fatalf("missing-binary gate evidence = %q, want the exec error surfaced", c.Evidence)
	}
}
