//go:build realclaude

// Package sentinel real-claude advisor runner (2C-1, ADR-0006 Katman-2): the OPT-IN
// production runner that drives the REAL `claude -p` CLI as the gray-zone liveness
// advisor. It is excluded from the default gate by the `realclaude` build tag and
// guarded again at call time by CP_REAL_CLAUDE=1, so the offline gate never spawns
// the CLI — the default build links only the stub runner (advisor_stub.go).
//
// It MIRRORS engine.execRunner (command_engine.go, FROZEN — not edited): direct
// exec.CommandContext, no shell, combined stdout+stderr, subscription auth ONLY
// (no API key, no --api-key flag), a tight timeout supplied by the caller's ctx.
// No secret is read or written.
//
// Wire it explicitly:
//
//	CP_REAL_CLAUDE=1 CP_CLAUDE_BIN=/path/to/claude   (then construct via NewRealCommandAdvisor)
package sentinel

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
)

// realAdvisorEnabled reports whether the real `claude -p` advisor is enabled. Even
// with the realclaude build tag compiled in, the advisor stays a no-op unless the
// operator explicitly sets CP_REAL_CLAUDE=1, so a tagged build cannot accidentally
// spawn the CLI.
func realAdvisorEnabled() bool {
	return os.Getenv("CP_REAL_CLAUDE") == "1"
}

// realClaudeBin resolves the claude binary: CP_CLAUDE_BIN if set, else "claude" on
// PATH. It mirrors the engine smoke test's resolution.
func realClaudeBin() string {
	if bin := os.Getenv("CP_CLAUDE_BIN"); bin != "" {
		return bin
	}
	return "claude"
}

// NewRealCommandAdvisor builds a CommandAdvisor wired to the REAL `claude -p`
// runner, for production gray-zone advising. It returns a configuration error if
// CP_REAL_CLAUDE is not set, so callers fail loud rather than silently building a
// disabled advisor. The invocation shape mirrors the engine's real claude usage:
//
//	claude -p <prompt-on-stdin> --dangerously-skip-permissions
//
// subscription auth only, tight timeout via the Assess ctx. The prompt is passed
// on stdin (AdvisorPrompt) rather than argv so the recent-output tail is not
// exposed on the process command line.
func NewRealCommandAdvisor(dir string) (*CommandAdvisor, error) {
	if !realAdvisorEnabled() {
		return nil, errors.New("sentinel: real claude advisor requires CP_REAL_CLAUDE=1")
	}
	bin := realClaudeBin()
	// -p reads the prompt; we feed it on stdin via the runner, so the prompt is the
	// final positional. --dangerously-skip-permissions: non-interactive (read-only
	// log inspection, no writes expected). No API key, no --api-key: subscription
	// auth only.
	argv := []string{bin, "-p", "--dangerously-skip-permissions"}
	return NewCommandAdvisor(argv, dir, nil, realClaudeRunner), nil
}

// realClaudeRunner is the production runnerFunc: it runs argv via
// exec.CommandContext in dir with the parent environment plus env, feeds the
// prompt on stdin, and returns combined stdout+stderr (auth/JSON markers can land
// on either). It is a direct mirror of engine.execRunner.
func realClaudeRunner(ctx context.Context, argv []string, dir string, stdin []byte, env []string) ([]byte, error) {
	if len(argv) == 0 {
		return nil, errors.New("sentinel: empty advisor command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // argv is the operator-supplied advisor command.
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = bytes.NewReader(stdin)

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.Bytes(), err
}
