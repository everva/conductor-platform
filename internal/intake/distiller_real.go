package intake

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"

	"github.com/everva/conductor-platform/internal/envsafe"
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

// clarifyPrompt is the ADDITIVE clarifying-mode instruction (ADR-0047): the model
// does EXACTLY ONE of (A) distill concrete scenarios when the conversation is
// specific enough, or (B) ask up to four specific multiple-choice clarifying
// questions when it is ambiguous — Claude Code's AskUserQuestion discipline ("ask,
// don't assume"). The deterministic ParseOutcome keys off the two fence pairs. It is
// strict so the gate-tested ParseQuestions / ParseScenarios contracts hold against
// the real model. The frozen distillPrompt above is untouched.
const clarifyPrompt = `You are the conductor-platform intake distiller (ADR-0005, ADR-0012, ADR-0018, ADR-0047)
operating in CLARIFYING mode. Read the conversation below and do EXACTLY ONE of the following.

(A) If the conversation gives you enough to specify one or more concrete, verifiable scenarios,
    emit your final answer as a single YAML block wrapped EXACTLY in these fences:
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

(B) If the conversation is ambiguous or underspecified, DO NOT GUESS. Ask the director up to four
    specific multiple-choice clarifying questions, wrapped EXACTLY in these fences:
    ` + questionsFenceStart + `
    questions:
      - question: <the specific question to resolve the ambiguity>
        header: <short chip label, 12 chars max>
        multi_select: false
        options:
          - label: <a short choice, 1-5 words>
            description: <what choosing this means>
          - label: <another short choice>
            description: <what choosing this means>
    ` + questionsFenceEnd + `

Rules (STRICT):
- Emit EITHER one ` + scenariosFenceStart + ` block OR one ` + questionsFenceStart + ` block — never both, never neither.
- Never fabricate scenarios just to avoid asking: when unsure, ASK (option B).
- Each question needs 2-4 options; each option needs a label and a description. The UI adds an
  "Other" free-text choice automatically, so do NOT add one. Use multi_select: true only when more
  than one option may apply.
- The hidden_holdout_ref MUST be repo-external (store:// , pg:// or private:). Never a repo path.
- Every scenario MUST have id, title, lane, tier, at least one acceptance bullet, and a holdout ref.

Conversation:
`

// NewCommandDistiller returns a CommandDistiller wired to the REAL `claude -p`
// CLI via subscription auth (no API key — ADR constraint). The conversation is fed
// on stdin behind a distillation prompt; the model's stdout is parsed by the
// deterministic ParseScenarios / ParseOutcome. This is the production path; the
// default offline gate uses the *WithRunner constructors with a stub and never
// reaches this. Both seams are wired: Distill uses distillPrompt, DistillOrClarify
// uses clarifyPrompt (ADR-0047).
func NewCommandDistiller() *CommandDistiller {
	return &CommandDistiller{run: claudeDistillRunner, clarifyRun: claudeClarifyRunner}
}

// claudeDistillRunner runs the REAL `claude -p` over distillPrompt+conversation,
// backing the frozen Distill path. UNCHANGED behavior (ADR-0047): the prompt is the
// scenarios-only distillPrompt.
func claudeDistillRunner(ctx context.Context, conversation string) ([]byte, error) {
	return claudeRunnerWithPrompt(ctx, distillPrompt, conversation)
}

// claudeClarifyRunner runs the REAL `claude -p` over clarifyPrompt+conversation,
// backing the additive DistillOrClarify path (the model may answer with scenarios OR
// clarifying questions).
func claudeClarifyRunner(ctx context.Context, conversation string) ([]byte, error) {
	return claudeRunnerWithPrompt(ctx, clarifyPrompt, conversation)
}

// claudeRunnerWithPrompt runs `claude -p` over prompt+conversation on stdin and
// returns its combined output. It sets NO API key and passes no --api-key flag: it
// relies on the operator's already-logged-in subscription `claude` (the same
// contract as engine.execRunner / the realclaude smoke test). It is the shared exec
// primitive behind both the distill and the clarify runners.
func claudeRunnerWithPrompt(ctx context.Context, prompt, conversation string) ([]byte, error) {
	if _, err := exec.LookPath("claude"); err != nil {
		return nil, fmt.Errorf("intake: claude CLI not found: %w", err)
	}
	cmd := exec.CommandContext(ctx, "claude", "-p") //nolint:gosec // fixed subscription claude command; no shell, no API key.
	// SECURITY (review F7): sanitize the inherited env before handing it to the
	// distiller subprocess, mirroring engine.execRunner's R-2 hardening. The
	// distiller runs inside the API gateway process, whose env carries
	// CONDUCTOR_API_TOKEN + CONDUCTOR_DSN — secrets the distiller never needs.
	// envsafe.Sanitize strips GH_TOKEN/GITHUB_TOKEN/CONDUCTOR_* while preserving the
	// subscription `claude` auth (oauth/keychain), so distillation still works.
	cmd.Env = envsafe.Sanitize(os.Environ())
	cmd.Stdin = bytes.NewReader([]byte(prompt + conversation))

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if err != nil && buf.Len() == 0 {
		return nil, errors.New("intake: claude produced no output: " + err.Error())
	}
	return buf.Bytes(), err
}
