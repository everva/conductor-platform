package intake

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Distiller is the assisted-distillation seam (ADR-0005, ADR-0012 §4): it turns a
// free-text conversation into a slice of structured, VALIDATED scenarios. The step
// is non-deterministic (an LLM does the distilling), so it is an interface — the
// gate-testable core (schema, validation, parsing of the model's output) is
// deterministic, and the real-LLM call lives behind this seam.
//
// Distill MUST NOT fabricate scenarios: if the model produces no parseable,
// valid scenarios it returns an error and an empty slice (never a hallucinated
// green). Returned scenarios are already shape-validated (each .Validate() passes);
// cross-scenario dep resolution still happens at intake time against the store.
type Distiller interface {
	// Distill converts a conversation transcript into validated scenarios.
	Distill(ctx context.Context, conversation string) ([]Scenario, error)
}

// ErrNoScenarios means the distiller output contained no parseable scenario
// block. It is the never-fake-green sentinel for the distiller: callers detect it
// with errors.Is and must treat it as "the model gave us nothing usable", not as
// an empty success.
var ErrNoScenarios = errors.New("intake: distiller produced no scenarios")

// ErrMalformedScenarios means a scenario block was found but did not parse or did
// not validate. It wraps the underlying decode/validation error.
var ErrMalformedScenarios = errors.New("intake: distiller produced malformed scenarios")

// distillRunner runs one distillation invocation and returns the model's raw
// stdout. It is the injectable subprocess seam (mirroring engine.runnerFunc):
// production wires it to `claude -p` (see distiller_real.go); tests pass a stub
// that returns crafted stdout WITHOUT spawning any real LLM, keeping the gate
// offline.
type distillRunner func(ctx context.Context, conversation string) (stdout []byte, err error)

// CommandDistiller is the CommandEngine-style Distiller: it runs a distillation
// command as a subprocess and deterministically parses the model's prose+YAML
// output into validated scenarios (the analogue of engine.CommandEngine +
// ParseVerdict, ADR-0014). It holds no LLM logic itself — the model runs in the
// subprocess — and never fabricates scenarios.
type CommandDistiller struct {
	// run backs the frozen Distill path (distillPrompt → scenarios only).
	run distillRunner
	// clarifyRun backs the additive DistillOrClarify path (clarifyPrompt →
	// scenarios OR clarifying questions). It is a SEPARATE runner because it
	// prepends a different prompt; the frozen Distill path is unaffected (ADR-0047).
	clarifyRun distillRunner
}

// Compile-time assertions that *CommandDistiller satisfies both seams.
var (
	_ Distiller           = (*CommandDistiller)(nil)
	_ ClarifyingDistiller = (*CommandDistiller)(nil)
)

// NewCommandDistillerWithRunner returns a CommandDistiller driven by the given
// runner. It is the test seam: a stub runner returns fixture stdout (valid YAML in
// prose, malformed, empty) with no real `claude -p` invocation. A nil runner is a
// programming error and is rejected at call time. It wires only the frozen Distill
// path; DistillOrClarify on the result errors until a clarify runner is set (use
// NewClarifyingDistillerWithRunner for that path).
func NewCommandDistillerWithRunner(run distillRunner) *CommandDistiller {
	return &CommandDistiller{run: run}
}

// NewClarifyingDistillerWithRunner returns a CommandDistiller whose DistillOrClarify
// path is driven by the given runner (ADR-0047 test seam). A stub runner returns
// fixture stdout (a fenced scenarios OR questions block) with no real `claude -p`.
func NewClarifyingDistillerWithRunner(clarifyRun distillRunner) *CommandDistiller {
	return &CommandDistiller{clarifyRun: clarifyRun}
}

// Distill runs the distillation subprocess over the conversation and parses its
// stdout into validated scenarios. Empty/error output yields ErrNoScenarios; a
// scenario block that fails to parse or validate yields ErrMalformedScenarios.
func (d *CommandDistiller) Distill(ctx context.Context, conversation string) ([]Scenario, error) {
	if d.run == nil {
		return nil, errors.New("intake: distiller has no runner configured")
	}
	if strings.TrimSpace(conversation) == "" {
		return nil, errors.New("intake: distiller: empty conversation")
	}
	stdout, runErr := d.run(ctx, conversation)
	if runErr != nil {
		// A run error with no usable output is a no-scenario outcome; with usable
		// output we still try to parse (the model may have emitted scenarios and
		// then exited non-zero), matching the engine's classifyOutput leniency.
		if len(strings.TrimSpace(string(stdout))) == 0 {
			return nil, fmt.Errorf("%w: %v", ErrNoScenarios, runErr)
		}
	}
	return ParseScenarios(stdout)
}

// DistillOrClarify runs the clarifying-distill subprocess (clarifyPrompt) and parses
// its stdout into a DistillOutcome — either validated scenarios or validated
// clarifying questions (ADR-0047). It mirrors Distill's leniency and never-fabricate
// discipline: a run error with empty output yields ErrNoScenarios; otherwise the
// output is parsed by ParseOutcome (which keeps the ErrNoScenarios sentinel for the
// "nothing usable" case so the gateway's 422 mapping is unchanged). It is ADDITIVE:
// the frozen Distill path above is not touched.
//
// When no clarify runner is configured (a distiller built only for the frozen
// Distill path, e.g. via NewCommandDistillerWithRunner) it gracefully DEGRADES to the
// distill runner: such a distiller never emits questions but still distills
// scenarios. This keeps *CommandDistiller a coherent ClarifyingDistiller for every
// constructor, so the gateway can type-assert the seam without surprise.
func (d *CommandDistiller) DistillOrClarify(ctx context.Context, conversation string) (DistillOutcome, error) {
	runner := d.clarifyRun
	if runner == nil {
		runner = d.run
	}
	if runner == nil {
		return DistillOutcome{}, errors.New("intake: distiller has no runner configured")
	}
	if strings.TrimSpace(conversation) == "" {
		return DistillOutcome{}, errors.New("intake: distiller: empty conversation")
	}
	stdout, runErr := runner(ctx, conversation)
	if runErr != nil && len(strings.TrimSpace(string(stdout))) == 0 {
		return DistillOutcome{}, fmt.Errorf("%w: %v", ErrNoScenarios, runErr)
	}
	return ParseOutcome(stdout)
}

// scenarioBlock is the wrapper the distiller is prompted to emit so its scenarios
// are unambiguously delimited inside prose: a YAML document with a top-level
// `scenarios:` list. Parsing keys off this wrapper rather than guessing which YAML
// span in the narration is the answer (the deterministic analogue of
// engine.lastResultObject's "last result-keyed object" rule).
type scenarioBlock struct {
	Scenarios []Scenario `yaml:"scenarios"`
}

// scenariosFenceStart and scenariosFenceEnd delimit the machine-readable block in
// the model's output so the parser can lift it out of surrounding prose. The model
// is instructed to wrap exactly its final answer in these markers.
const (
	scenariosFenceStart = "<<<SCENARIOS"
	scenariosFenceEnd   = "SCENARIOS>>>"
)

// ParseScenarios is the deterministic distiller-output parser: it extracts the
// fenced scenario block from prose-wrapped model output, decodes it as a YAML
// `scenarios:` list, and validates every scenario in isolation. It NEVER coerces
// unusable output into scenarios:
//
//   - no fenced block, or an empty list  -> ErrNoScenarios;
//   - a block that fails to YAML-decode   -> ErrMalformedScenarios;
//   - a scenario that fails .Validate()   -> ErrMalformedScenarios (wraps the
//     ValidationError).
//
// On success it returns the validated scenarios in output order. Cross-scenario
// dep resolution is deliberately NOT done here (it needs the store's known IDs);
// it runs at intake time via ValidateSet.
func ParseScenarios(stdout []byte) ([]Scenario, error) {
	block, ok := extractScenarioBlock(string(stdout))
	if !ok {
		return nil, fmt.Errorf("%w: no %s..%s block in output", ErrNoScenarios, scenariosFenceStart, scenariosFenceEnd)
	}

	var parsed scenarioBlock
	if err := yaml.Unmarshal([]byte(block), &parsed); err != nil {
		return nil, fmt.Errorf("%w: decode block: %v", ErrMalformedScenarios, err)
	}
	if len(parsed.Scenarios) == 0 {
		return nil, fmt.Errorf("%w: block has an empty scenarios list", ErrNoScenarios)
	}
	for i := range parsed.Scenarios {
		if err := parsed.Scenarios[i].Validate(); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrMalformedScenarios, err)
		}
	}
	return parsed.Scenarios, nil
}

// extractScenarioBlock returns the text between the LAST scenarios start fence and
// the next end fence after it, so a model that narrates, drafts, then emits its
// final block last wins (the last-block rule, mirroring engine.lastResultObject). It
// returns ok=false when no complete fenced block exists.
func extractScenarioBlock(out string) (string, bool) {
	return extractFenced(out, scenariosFenceStart, scenariosFenceEnd)
}

// extractFenced returns the trimmed text between the LAST startFence and the next
// endFence after it (the last-block rule). It returns ok=false when no complete,
// non-empty fenced block exists. It is the shared primitive behind both the
// scenarios and the questions (ADR-0047) block extraction.
func extractFenced(out, startFence, endFence string) (string, bool) {
	start := strings.LastIndex(out, startFence)
	if start < 0 {
		return "", false
	}
	rest := out[start+len(startFence):]
	end := strings.Index(rest, endFence)
	if end < 0 {
		return "", false
	}
	block := strings.TrimSpace(rest[:end])
	if block == "" {
		return "", false
	}
	return block, true
}
