package intake

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

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

// ClaudeDistillerConfig configures the production `claude -p` distiller (Faz-R / k8s).
// Both fields are OPTIONAL and ADDITIVE — a zero config reproduces the original behavior
// (subscription auth inherited from the env, the model's default), so NewCommandDistiller()
// is unchanged.
type ClaudeDistillerConfig struct {
	// TokenProvider, when set, is called PER INVOCATION to resolve the subscription
	// CLAUDE_CODE_OAUTH_TOKEN, which is injected into ONLY the distiller subprocess's env
	// (never os.Setenv / the gateway's long-lived env) — the Option-3 model (ADR INTAKE-CLAUDE-
	// IN-K8S, docs/decisions): per-call decrypt of the gateway's sealed claude credential, the
	// narrowest exposure. nil → the subprocess inherits whatever subscription auth is already in
	// the env (the local davinci path). The provider returns the plaintext token; "" → no token
	// injected (the env's own auth, if any, still applies).
	TokenProvider func(ctx context.Context) (string, error)
	// Model, when non-empty, pins `claude -p --model <Model>` so the distiller runs on a SPECIFIC
	// model (e.g. claude-opus-4-8). "" → the subscription's default model (davinci parity, no
	// --model flag). Operator-configured (env) so the choice lives in deploy config, not code.
	Model string
}

// claudeOptions is the internal, threaded form of ClaudeDistillerConfig — carried into the exec
// helpers so the arg + env construction is pure and unit-testable without spawning claude.
type claudeOptions struct {
	tokenProvider func(ctx context.Context) (string, error)
	model         string
}

// NewCommandDistiller returns a CommandDistiller wired to the REAL `claude -p` CLI via
// subscription auth (no API key — ADR constraint), inheriting the env's auth and the model's
// default. The conversation is fed on stdin behind a distillation prompt; the model's stdout is
// parsed by the deterministic ParseScenarios / ParseOutcome. This is the local production path
// (davinci); the default offline gate uses the *WithRunner constructors with a stub.
func NewCommandDistiller() *CommandDistiller {
	return newClaudeDistiller(claudeOptions{})
}

// NewCommandDistillerWithClaude is NewCommandDistiller with explicit Faz-R config: a per-call
// token provider (the k8s sealed-credential path) and/or a pinned model. A zero config behaves
// EXACTLY like NewCommandDistiller (frozen). The gateway wires the provider from its credential
// store + sealer so `/distill` works in a pod with no ambient claude login.
func NewCommandDistillerWithClaude(cfg ClaudeDistillerConfig) *CommandDistiller {
	return newClaudeDistiller(claudeOptions{tokenProvider: cfg.TokenProvider, model: cfg.Model})
}

// newClaudeDistiller builds a CommandDistiller whose three runners share the given options. The
// runners are thin closures over the shared exec primitives so the distill / clarify / stream
// paths stay behavior-identical except for the prompt + the (additive) opts.
func newClaudeDistiller(opts claudeOptions) *CommandDistiller {
	return &CommandDistiller{
		run: func(ctx context.Context, conversation string) ([]byte, error) {
			return claudeRunnerWithPrompt(ctx, distillPrompt, conversation, opts)
		},
		clarifyRun: func(ctx context.Context, conversation string) ([]byte, error) {
			return claudeRunnerWithPrompt(ctx, clarifyPrompt, conversation, opts)
		},
		clarifyStreamRun: func(ctx context.Context, conversation string, onLine func(string)) ([]byte, error) {
			return claudeStreamRunnerWithPrompt(ctx, clarifyPrompt, conversation, onLine, opts)
		},
	}
}

// claudeArgs builds the `claude` argv for a print invocation, appending `--model <m>` only when a
// model is pinned. Pure (no exec) so a test pins the exact argv. Always starts with `-p`.
func claudeArgs(opts claudeOptions) []string {
	args := []string{"-p"}
	if strings.TrimSpace(opts.model) != "" {
		args = append(args, "--model", opts.model)
	}
	return args
}

// claudeEnv builds the subprocess env: the SANITIZED inherited env (review F7 — strips
// CONDUCTOR_*/GH_TOKEN while preserving the subscription claude auth), plus, when a token provider
// is set, a per-call CLAUDE_CODE_OAUTH_TOKEN appended to THIS slice only (never os.Setenv — the
// gateway's long-lived env is untouched, Option 3). A provider error aborts the run (so a missing/
// unopenable credential surfaces as a distill failure, not a silent unauthenticated call). Pure +
// unit-testable: it returns the env slice without spawning anything.
func claudeEnv(ctx context.Context, opts claudeOptions) ([]string, error) {
	env := envsafe.Sanitize(os.Environ())
	if opts.tokenProvider != nil {
		token, err := opts.tokenProvider(ctx)
		if err != nil {
			return nil, fmt.Errorf("intake: distiller credential: %w", err)
		}
		if token != "" {
			env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+token)
		}
	}
	return env, nil
}

// claudeRunnerWithPrompt runs `claude -p` over prompt+conversation on stdin and returns its
// combined output. It sets NO API key and passes no --api-key flag: it relies on the subscription
// `claude` auth (env-inherited, or the per-call token injected via opts for the k8s path). The
// shared exec primitive behind the distill and clarify runners.
func claudeRunnerWithPrompt(ctx context.Context, prompt, conversation string, opts claudeOptions) ([]byte, error) {
	if _, err := exec.LookPath("claude"); err != nil {
		return nil, fmt.Errorf("intake: claude CLI not found: %w", err)
	}
	env, err := claudeEnv(ctx, opts)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "claude", claudeArgs(opts)...) //nolint:gosec // fixed subscription claude command; no shell, no API key.
	cmd.Env = env
	cmd.Stdin = bytes.NewReader([]byte(prompt + conversation))

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	runErr := cmd.Run()
	if runErr != nil && buf.Len() == 0 {
		return nil, errors.New("intake: claude produced no output: " + runErr.Error())
	}
	// Pass the run error THROUGH alongside any output: a non-zero exit with a usable block still
	// parses (the engine/distiller leniency in Distill/ParseOutcome).
	return buf.Bytes(), runErr
}

// claudeStreamRunnerWithPrompt runs `claude -p` over prompt+conversation, streaming
// stdout lines through onLine as they arrive and returning the full combined output.
// Same auth/env contract as claudeRunnerWithPrompt (subscription claude, sanitized
// env + the optional per-call token + pinned model via opts). stdout is line-scanned for
// progress; stderr is captured and appended after so a non-zero exit with a usable block
// still parses (engine leniency). The fenced answer lives in stdout, so ParseOutcome's
// last-fence rule is unaffected by ordering.
func claudeStreamRunnerWithPrompt(ctx context.Context, prompt, conversation string, onLine func(string), opts claudeOptions) ([]byte, error) {
	if _, err := exec.LookPath("claude"); err != nil {
		return nil, fmt.Errorf("intake: claude CLI not found: %w", err)
	}
	env, err := claudeEnv(ctx, opts)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "claude", claudeArgs(opts)...) //nolint:gosec // fixed subscription claude command; no shell, no API key.
	cmd.Env = env
	cmd.Stdin = bytes.NewReader([]byte(prompt + conversation))

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("intake: stdout pipe: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("intake: start claude: %w", err)
	}

	var acc bytes.Buffer
	scanner := bufio.NewScanner(stdoutPipe)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // tolerate long lines (1 MiB)
	for scanner.Scan() {
		line := scanner.Text()
		acc.WriteString(line)
		acc.WriteByte('\n')
		if onLine != nil {
			onLine(line)
		}
	}
	scanErr := scanner.Err()
	waitErr := cmd.Wait()
	if stderr.Len() > 0 {
		acc.Write(stderr.Bytes())
	}

	out := acc.Bytes()
	runErr := waitErr
	if runErr == nil {
		runErr = scanErr
	}
	if runErr != nil && len(out) == 0 {
		return nil, errors.New("intake: claude produced no output: " + runErr.Error())
	}
	return out, runErr
}
