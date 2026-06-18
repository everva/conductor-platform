// Package verify implements the independent, deterministic verify gate of
// Faz-1a (ADR-0003 evidence-based, ADR-0018 holdout isolation, Rule#9). It runs
// the recipe gate commands (build/test/vet/lint) over the performer's output to
// produce engine.Check evidence, injects the REPO-EXTERNAL hidden holdout into a
// SEPARATE throwaway verify-worktree, runs it, and folds both into an
// engine.ReviewResult.
//
// It is INDEPENDENT: the result is derived from the gate exit codes and the
// holdout outcome, NOT from the performer's self-reported Verdict.Result
// (Rule#9). It NEVER fakes green — a holdout-breaking commit yields
// changes-requested. It MUST NOT change the frozen Check/ReviewResult field sets
// (PRE-0).
//
// Isolation (ADR-0018): the holdout is the only thing injected, and only into a
// verify-worktree that is removed after the run (even on failure). The develop
// worktree and the performer's branch are never written to. The holdout source
// and contents never leak into the develop worktree or the returned Findings.
package verify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/everva/conductor-platform/internal/engine"
)

// reviewPass / reviewChanges are the frozen ReviewResult.Result values
// (ADR-0003). checkPass / checkFail are the frozen Check.Result values.
const (
	reviewPass    = "pass"
	reviewChanges = "changes-requested"
	checkPass     = "pass"
	checkFail     = "fail"
)

// Holdout is a fetched hidden-holdout payload: a non-revealing identifier plus
// the files to copy temporarily into the verify-worktree. Contents stay internal
// to the verify run and never surface in Findings or the develop worktree.
type Holdout struct {
	// Name is a non-revealing identifier (e.g. "B-2") used only for findings.
	Name string
	// Files maps worktree-relative paths to file contents to inject.
	Files map[string][]byte
}

// HoldoutStore is the injected, repo-EXTERNAL source of hidden holdouts
// (ADR-0018). It is an interface so Faz-1b can swap the local fake for a
// Postgres/private-repo backing without touching the verifier. The performer
// never sees it.
type HoldoutStore interface {
	// Fetch returns the holdout referenced by ref (e.g. a Scenario.HoldoutRef).
	Fetch(ctx context.Context, ref string) (Holdout, error)
}

// Gate is one deterministic recipe gate command (build/test/vet/lint). Argv is
// program + args, executed without a shell.
type Gate struct {
	// Name is the gate's name surfaced in the Check (e.g. "go build").
	Name string
	// Argv is the command to run (program + args).
	Argv []string
}

// Config configures a Verifier. HoldoutCmd is the argv used to run the injected
// holdout suite inside the verify-worktree.
type Config struct {
	// HoldoutCmd is the argv that runs the holdout suite (program + args),
	// executed in the verify-worktree after the holdout files are injected.
	HoldoutCmd []string

	// onVerifyWorktree, when set, is invoked with the verify-worktree path right
	// after it is created. Test-only seam to assert creation + cleanup.
	onVerifyWorktree func(path string)
}

// Verifier runs the independent deterministic verify gate. Construct it with New
// and an injected HoldoutStore; it holds no global state.
type Verifier struct {
	store HoldoutStore
	cfg   Config
}

// New returns a Verifier backed by the injected holdout store and config.
func New(store HoldoutStore, cfg Config) *Verifier {
	return &Verifier{store: store, cfg: cfg}
}

// Verify runs every gate over the workspace to produce engine.Check evidence,
// injects and runs the hidden holdout in a throwaway verify-worktree, and folds
// both into an engine.ReviewResult (ADR-0003, ADR-0018). The performer's
// self-reported verdict.Result is IGNORED (Rule#9): the decision comes only from
// the gate exit codes and holdout outcome.
//
// It returns the ReviewResult and the gate Checks. The verify-worktree (and the
// injected holdout) is always removed before returning, even on error.
func (v *Verifier) Verify(ctx context.Context, _ engine.Verdict, ws engine.Workspace, gates []Gate, holdoutRef string) (engine.ReviewResult, []engine.Check, error) {
	if ws.Path == "" {
		return engine.ReviewResult{}, nil, errors.New("verify: empty workspace path")
	}

	// 1. Deterministic recipe gates over the performer's output -> Checks.
	checks := make([]engine.Check, 0, len(gates))
	var findings []string
	for _, g := range gates {
		c := runGate(ctx, ws.Path, g)
		checks = append(checks, c)
		if c.Result != checkPass {
			findings = append(findings, fmt.Sprintf("gate %q failed: %s", c.Name, c.Evidence))
		}
	}

	// 2. Hidden-holdout in a SEPARATE verify-worktree (ADR-0018). A failing
	// holdout contributes findings; the boolean is folded into len(findings).
	holdoutFindings, err := v.runHoldout(ctx, ws, holdoutRef)
	if err != nil {
		return engine.ReviewResult{}, checks, fmt.Errorf("verify: holdout: %w", err)
	}
	findings = append(findings, holdoutFindings...)

	// 3. Independent result: pass only if every gate AND the holdout pass.
	result := reviewPass
	summary := "all gates and holdout passed"
	if len(findings) > 0 {
		result = reviewChanges
		summary = fmt.Sprintf("%d gate/holdout finding(s)", len(findings))
	}

	return engine.ReviewResult{Result: result, Findings: findings, Summary: summary}, checks, nil
}

// runGate runs a single gate command in the workspace and maps its exit code to
// a frozen engine.Check. Evidence is a short, factual record (first failing line
// of trimmed output) and never the full dump.
func runGate(ctx context.Context, dir string, g Gate) engine.Check {
	if len(g.Argv) == 0 {
		return engine.Check{Name: g.Name, Result: checkFail, Evidence: "no command configured"}
	}
	cmd := exec.CommandContext(ctx, g.Argv[0], g.Argv[1:]...) //nolint:gosec // argv is the operator-supplied recipe gate.
	cmd.Dir = dir
	cmd.Env = sanitizedEnv()
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if err != nil {
		// A configured gate whose binary is MISSING (e.g. golangci-lint not installed)
		// fails to even start: cmd.Run returns an exec error with NO captured output.
		// Surface that error as the Evidence so the failure is a clear, deterministic
		// "binary missing" signal rather than an opaque "no output" — the gate FAILS,
		// it is never silently skipped (Rule#9, no fake-green).
		evidence := firstLine(buf.Bytes())
		if buf.Len() == 0 {
			evidence = firstLine([]byte(err.Error()))
		}
		return engine.Check{Name: g.Name, Result: checkFail, Evidence: evidence}
	}
	return engine.Check{Name: g.Name, Result: checkPass, Evidence: "exit 0"}
}

// runHoldout creates a SEPARATE verify-worktree from the performer's commit,
// injects the repo-external holdout files, runs the holdout suite, and removes
// the verify-worktree (always, via defer). It returns whether the holdout
// passed plus non-revealing findings; it never copies the holdout into the
// develop worktree and never leaks holdout contents into findings.
func (v *Verifier) runHoldout(ctx context.Context, ws engine.Workspace, ref string) (findings []string, err error) {
	// An empty locator means the scenario carries no repo-external holdout
	// (ADR-0018): skip the holdout leg cleanly so a non-holdout project verifies on
	// its public gates alone. This is additive and backward compatible — existing
	// callers pass a non-empty ref, so the store is still consulted as before.
	if strings.TrimSpace(ref) == "" {
		return nil, nil
	}

	holdout, ferr := v.store.Fetch(ctx, ref)
	if ferr != nil {
		return nil, fmt.Errorf("fetch holdout: %w", ferr)
	}

	// A store may resolve an empty ref to an empty holdout (no files): with no
	// files to inject and nothing to run, skip the verify-worktree dance entirely.
	if len(holdout.Files) == 0 {
		return nil, nil
	}

	// Throwaway verify-worktree as a sibling of the develop worktree, cut from
	// the performer's exact HEAD so the holdout sees the reviewed code.
	parent := filepath.Dir(ws.Path)
	vwt, err := os.MkdirTemp(parent, "conductor-verify-")
	if err != nil {
		return nil, fmt.Errorf("create verify-worktree dir: %w", err)
	}
	// os.MkdirTemp made the dir; git worktree add needs it absent.
	if rmErr := os.RemoveAll(vwt); rmErr != nil {
		return nil, fmt.Errorf("prepare verify-worktree: %w", rmErr)
	}
	if v.cfg.onVerifyWorktree != nil {
		v.cfg.onVerifyWorktree(vwt)
	}

	// Cleanup is defer-guaranteed even on a failing/panicking gate (ADR-0018).
	defer func() {
		_ = runGit(ctx, ws.Path, "worktree", "remove", "--force", vwt)
		_ = os.RemoveAll(vwt)
		_ = runGit(ctx, ws.Path, "worktree", "prune")
	}()

	if gerr := runGit(ctx, ws.Path, "worktree", "add", "--detach", vwt, "HEAD"); gerr != nil {
		return nil, fmt.Errorf("add verify-worktree: %w", gerr)
	}

	// Inject the holdout files TEMPORARILY into the verify-worktree only.
	for rel, content := range holdout.Files {
		dst := filepath.Join(vwt, filepath.Clean(rel))
		if !strings.HasPrefix(dst, filepath.Clean(vwt)+string(os.PathSeparator)) {
			return nil, fmt.Errorf("holdout path escapes verify-worktree: %q", rel)
		}
		if mkErr := os.MkdirAll(filepath.Dir(dst), 0o755); mkErr != nil {
			return nil, fmt.Errorf("prepare holdout dir: %w", mkErr)
		}
		if wErr := os.WriteFile(dst, content, 0o644); wErr != nil { //nolint:gosec // throwaway verify-worktree fixture.
			return nil, fmt.Errorf("inject holdout: %w", wErr)
		}
	}

	if len(v.cfg.HoldoutCmd) == 0 {
		return nil, errors.New("no holdout command configured")
	}
	cmd := exec.CommandContext(ctx, v.cfg.HoldoutCmd[0], v.cfg.HoldoutCmd[1:]...) //nolint:gosec // operator-supplied holdout runner.
	cmd.Dir = vwt
	cmd.Env = sanitizedEnv()
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if runErr := cmd.Run(); runErr != nil {
		// Non-revealing finding: identify the holdout, never echo its contents.
		return []string{fmt.Sprintf("hidden holdout %q failed (exit non-zero)", nonRevealing(holdout.Name, ref))}, nil
	}
	return nil, nil
}

// nonRevealing returns a stable, non-revealing holdout identifier for findings:
// the holdout name if set, else the bare ref. Holdout file contents are never
// included.
func nonRevealing(name, ref string) string {
	if name != "" {
		return name
	}
	return ref
}

// sanitizedEnv returns the current process environment with every CONDUCTOR_*
// variable removed. The verify gate runs `go build`/`go test`/etc. as
// subprocesses over the GATED project; if the daemon's own config env (e.g.
// CONDUCTOR_DSN) leaked into that project's process it could silently change the
// project's behavior and break its OWN env-reading tests (a fake-red). We strip
// only the daemon's `CONDUCTOR_`-prefixed config vars and keep everything else
// (PATH, HOME, GOPATH, GOCACHE, …) so the toolchain still works. The daemon's env
// is read once per gate command; it is not mutated.
func sanitizedEnv() []string {
	src := os.Environ()
	out := make([]string, 0, len(src))
	for _, kv := range src {
		// Each entry is "KEY=VALUE"; strip those whose KEY starts with CONDUCTOR_.
		if strings.HasPrefix(kv, "CONDUCTOR_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// firstLine returns the first non-empty trimmed line of out, capped, as factual
// gate Evidence.
func firstLine(out []byte) string {
	for _, line := range bytes.Split(out, []byte("\n")) {
		t := strings.TrimSpace(string(line))
		if t == "" {
			continue
		}
		if len(t) > 200 {
			t = t[:200]
		}
		return t
	}
	return "exit non-zero (no output)"
}

// runGit runs a git command rooted at dir (the develop worktree's repo, which
// owns the sibling verify-worktree) and wraps failures with output.
func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %v: %w: %s", args, err, out)
	}
	return nil
}
