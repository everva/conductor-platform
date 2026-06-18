//go:build realclaude

// Real `claude -p` gray-zone ADVISOR smoke (review sweep S-3, ADR-0006 Katman-2).
// It closes the coverage gap that the only live exercise of the advisor path was
// the engine's develop smoke — the SENTINEL advisor runner (advisor_realclaude.go)
// had no live test. Like the engine smoke it is double-gated and excluded from the
// normal gate:
//   - build tag `realclaude` — excluded from `go test ./...` and `-tags e2e`;
//   - env guard CP_REAL_CLAUDE=1 — even with the tag, the test SKIPS unless set.
//
// Run it explicitly:
//
//	CP_REAL_CLAUDE=1 CP_CLAUDE_BIN=/path/to/claude \
//	  go test -tags realclaude -run RealClaudeAdvisor -v ./internal/sentinel/
//
// It proves the production advisor drives the real CLI and the deterministic
// ParseAdvice maps the REAL output onto a valid 3-valued Advice (or surfaces a
// clean error) — never a fabricated/invalid verdict.
package sentinel

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// stalledOutputSample is a realistic "stalled run" log tail handed to the advisor:
// a build that has emitted nothing actionable for a while. The advisor must judge
// whether this is still progressing, stuck, or needs a human — a narrow liveness
// call, NOT a quality judgement.
const stalledOutputSample = `[performer] go test ./...
[performer] ok  	example/pkg/a	0.01s
[performer] (no output for 6m — waiting on a long integration test)
`

// TestRealClaudeAdvisor_AssessReturnsValidAdvice runs the REAL claude advisor once
// and asserts the gray-zone path yields a VALID 3-valued Advice (progressing/stuck/
// needs_human) or a cleanly-surfaced error — never an out-of-enum/fabricated value.
func TestRealClaudeAdvisor_AssessReturnsValidAdvice(t *testing.T) {
	if os.Getenv("CP_REAL_CLAUDE") != "1" {
		t.Skip("real claude advisor smoke is opt-in: set CP_REAL_CLAUDE=1 to run it")
	}
	claudeBin := os.Getenv("CP_CLAUDE_BIN")
	if claudeBin == "" {
		claudeBin = "claude"
	}
	if _, err := exec.LookPath(claudeBin); err != nil {
		if _, statErr := os.Stat(claudeBin); statErr != nil {
			t.Skipf("claude binary %q not found: %v", claudeBin, err)
		}
	}

	adv, err := NewRealCommandAdvisor(t.TempDir())
	if err != nil {
		t.Fatalf("NewRealCommandAdvisor: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	advice, reason, err := adv.Advise(ctx, stalledOutputSample)
	if err != nil {
		// A surfaced error (auth expired, parse failure, timeout) is acceptable — the
		// advisor never fabricates; the sentinel treats advisor trouble conservatively
		// (Continue until the Layer-3 backstop). Report it for visibility.
		t.Logf("advisor returned a surfaced error (acceptable, conservatively handled): %v", err)
		return
	}
	if !advice.Valid() {
		t.Fatalf("advisor returned out-of-enum advice %q (reason=%q) — must be progressing/stuck/needs_human", advice, reason)
	}
	t.Logf("real claude advisor advice=%q reason=%q", advice, reason)
}
