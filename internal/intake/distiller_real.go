package intake

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// distillPrompt is the distillation instruction prepended to the conversation. It
// pins the model to emit EXACTLY one fenced scenario block (the deterministic
// parser keys off these fences) in the rich schema, with a repo-EXTERNAL
// hidden_holdout_ref (ADR-0018). It is intentionally strict so the gate-tested
// ParseScenarios contract holds against the real model.
const distillPrompt = `You are the conductor-platform intake distiller (ADR-0005, ADR-0012, ADR-0018).
Turn the conversation below into one or more structured scenarios.

Output rules (STRICT):
- Emit your final answer as a single YAML block wrapped EXACTLY in the fences:
  ` + scenariosFenceStart + `
  scenarios:
    - id: <stable id, e.g. A-1>
      title: <one-line summary>
      lane: <capability lane>
      tier: <one of T1,T2,T3,T4>
      deps: [<dependency ids>]
      acceptance:
        - <machine-verifiable acceptance criterion>
      hidden_holdout_ref: "store://holdouts/<id>/<file>"   # repo-EXTERNAL only
  ` + scenariosFenceEnd + `
- The hidden_holdout_ref MUST be repo-external (store:// , pg:// or private:). Never a repo path.
- Every scenario MUST have id, title, lane, tier, at least one acceptance bullet, and a holdout ref.
- If you cannot produce a valid scenario, emit no fenced block.

Conversation:
`

// NewCommandDistiller returns a CommandDistiller wired to the REAL `claude -p`
// CLI via subscription auth (no API key — ADR constraint). The conversation is fed
// on stdin behind the distillation prompt; the model's stdout is parsed by the
// deterministic ParseScenarios. This is the production path; the default offline
// gate uses NewCommandDistillerWithRunner with a stub and never reaches this.
func NewCommandDistiller() *CommandDistiller {
	return NewCommandDistillerWithRunner(claudeDistillRunner)
}

// claudeDistillRunner runs `claude -p` over the prompt+conversation on stdin and
// returns its combined output. It sets NO API key and passes no --api-key flag: it
// relies on the operator's already-logged-in subscription `claude` (the same
// contract as engine.execRunner / the realclaude smoke test).
func claudeDistillRunner(ctx context.Context, conversation string) ([]byte, error) {
	if _, err := exec.LookPath("claude"); err != nil {
		return nil, fmt.Errorf("intake: claude CLI not found: %w", err)
	}
	cmd := exec.CommandContext(ctx, "claude", "-p") //nolint:gosec // fixed subscription claude command; no shell, no API key.
	cmd.Env = os.Environ()
	cmd.Stdin = bytes.NewReader([]byte(distillPrompt + conversation))

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if err != nil && buf.Len() == 0 {
		return nil, errors.New("intake: claude produced no output: " + err.Error())
	}
	return buf.Bytes(), err
}
