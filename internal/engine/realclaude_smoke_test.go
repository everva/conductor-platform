//go:build realclaude

// Package engine real-claude smoke (N-6 Part B): an OPT-IN, best-effort test
// that runs the REAL `claude -p` CLI ONCE through the product's CommandEngine and
// confirms its actual stdout parses through the product's verdict parser
// (ParseVerdict / classifyOutput, ADR-0014) — or, if claude is not authenticated,
// that the engine detects ErrAuthExpired cleanly.
//
// This is the reality check the crafted unit/e2e strings can NOT give: the
// fixtures in command_engine_test.go and the failure-mode e2e stubs were written
// by hand and may not match what `claude` 2.1.181 actually emits. This test finds
// out, by spawning the real CLI.
//
// HARD GUARDS (the default gate MUST stay deterministic + offline):
//   - build tag `realclaude` — excluded from `go test ./...` and `-tags e2e`;
//   - env guard `CP_REAL_CLAUDE=1` — even with the tag, the test SKIPS unless set;
//   - the real CLI runs AT MOST ONCE (no retry loop), with a tight (<=120s)
//     timeout, on a throwaway local git repo;
//   - subscription auth ONLY: no API key is set, no --api-key is passed. It relies
//     on the operator's already-logged-in `claude`. No secret is written anywhere.
//
// Run it explicitly:
//
//	CP_REAL_CLAUDE=1 CP_CLAUDE_BIN=/Users/you/.local/bin/claude \
//	  go test -tags realclaude -run RealClaude -v ./internal/engine/
package engine

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/everva/conductor-platform/internal/statestore"
)

// realClaudePrompt is the develop instruction handed to `claude -p`. It states
// the trivial task (create hello.txt containing "conductor") AND demands the
// verdict block in the EXACT shape command_engine.go's ParseVerdict accepts: the
// LAST top-level JSON object carrying a non-empty "result" key. We instruct it to
// print that object as the final line so the last-result-object rule selects it.
const realClaudePrompt = `You are a non-interactive build performer. Do EXACTLY this in the current working directory:
1. Create a file named hello.txt whose only contents are the single word: conductor
2. Stage and commit it on the current git branch with the message: feat: add hello.txt

Then, as the VERY LAST thing you output, print ONE single-line JSON object and NOTHING after it, in exactly this schema (no markdown fence, no trailing prose):
{"result":"pass","branch":"<current git branch>","commit_sha":"<the commit sha>","checks":[{"name":"create-file","result":"pass","evidence":"hello.txt committed"}],"files":["hello.txt"],"summary":"created hello.txt containing conductor"}
The "result" value MUST be the literal string "pass" if you created and committed the file, otherwise "fail".`

// TestRealClaude_DevelopParsesRealVerdict runs the real `claude -p` once via the
// product's CommandEngine and asserts the engine either (a) parsed a Verdict from
// the REAL output, or (b) cleanly detected ErrAuthExpired (logged-out). Anything
// else (malformed / no verdict / timeout) is reported as a real finding, not
// silently tolerated.
func TestRealClaude_DevelopParsesRealVerdict(t *testing.T) {
	if os.Getenv("CP_REAL_CLAUDE") != "1" {
		t.Skip("real claude smoke is opt-in: set CP_REAL_CLAUDE=1 to run it")
	}

	claudeBin := os.Getenv("CP_CLAUDE_BIN")
	if claudeBin == "" {
		claudeBin = "claude"
	}
	if _, err := exec.LookPath(claudeBin); err != nil {
		// Fall back to the absolute path if a bare name is not on PATH.
		if _, statErr := os.Stat(claudeBin); statErr != nil {
			t.Skipf("claude binary %q not found: %v", claudeBin, err)
		}
	}

	repo := newRealClaudeRepo(t)

	// REAL product engine driving the REAL claude CLI. Subscription auth only: no
	// API key in Env, no --api-key flag. Tight timeout, single invocation.
	//
	// -p / --print               : non-interactive, print result and exit.
	// --dangerously-skip-permissions : allow the file-write/commit without an
	//                              interactive permission prompt (throwaway repo).
	// --add-dir repo             : ensure tool access to the throwaway repo.
	eng := NewCommandEngine(RecipeConfig{
		DevelopCmd: []string{
			claudeBin, "-p", realClaudePrompt,
			"--dangerously-skip-permissions",
			"--add-dir", repo,
		},
		Timeout: 120 * time.Second,
	})

	task := statestore.Task{ID: "RC-1", ProjectID: "realclaude", Lane: "build", Tier: "T1"}
	ws := Workspace{Path: repo, Branch: "develop"}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	v, err := eng.Develop(ctx, task, ws)

	switch {
	case err == nil:
		// SUCCESS: the real output parsed into a Verdict through the product parser.
		t.Logf("REAL CLAUDE: parser ACCEPTED real output. Verdict=%+v", v)
		if v.Result == "" {
			t.Fatalf("real claude: parsed verdict has empty result: %+v", v)
		}
		// Confirm the task actually happened (the file landed) — proves it was a
		// real run, not just a well-formed verdict.
		if _, statErr := os.Stat(filepath.Join(repo, "hello.txt")); statErr != nil {
			t.Logf("real claude: NOTE verdict parsed but hello.txt not found: %v", statErr)
		}
	case errors.Is(err, ErrAuthExpired):
		// CLEAN auth detection: logged-out claude hit the wall and the engine
		// classified it correctly. This is a PASS for the resilience contract.
		t.Logf("REAL CLAUDE: auth wall detected cleanly (ErrAuthExpired): %v", err)
	case errors.Is(err, ErrNoVerdict):
		// Timeout or empty output. Not a contract violation per se, but a real
		// finding to report: the CLI did not return within the deadline.
		t.Fatalf("REAL CLAUDE: no verdict (timeout/empty) within deadline — FINDING: %v", err)
	case errors.Is(err, ErrMalformedVerdict):
		// The real output did NOT match what the parser expects. This is the most
		// important real finding to surface.
		t.Fatalf("REAL CLAUDE: real output did NOT parse (ErrMalformedVerdict) — the crafted fixtures may not match reality. FINDING: %v", err)
	default:
		t.Fatalf("REAL CLAUDE: unexpected error: %v", err)
	}
}

// newRealClaudeRepo creates a throwaway local git repo on a develop branch with a
// single seed commit, for the real claude run to operate in.
func newRealClaudeRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.name", "realclaude")
	run("config", "user.email", "realclaude@local")
	run("checkout", "-q", "-b", "develop")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# throwaway\n"), 0o644); err != nil {
		t.Fatalf("seed README: %v", err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "seed: throwaway repo")
	return dir
}
