package sentinel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Advisor is the gray-zone liveness ADVISOR seam (ADR-0006 Katman-2). It is the
// ONLY place an LLM enters the sentinel, and it is an ADVISOR, never the decision
// authority: Assess maps its verdict onto a Decision but the Layer-3 backstop is
// checked first and binds regardless. A nil Advisor is fully supported (Layers
// 1+3 only), so callers opt into the gray-zone intelligence.
//
// Advise is given the tail of the stalled run's output and returns one of the
// three enum verdicts plus a one-line reason. It returns an error on any trouble
// (LLM unavailable, malformed output, out-of-enum verdict); Assess treats an
// error as the CONSERVATIVE Continue and NEVER fakes advice. It must be safe for
// the caller's concurrency.
type Advisor interface {
	Advise(ctx context.Context, lastOutput string) (advice Advice, reason string, err error)
}

// AdvisorFunc adapts a plain function to the Advisor interface (handy for tests
// and for wiring a closure without a named type).
type AdvisorFunc func(ctx context.Context, lastOutput string) (Advice, string, error)

// Advise calls the underlying function.
func (f AdvisorFunc) Advise(ctx context.Context, lastOutput string) (Advice, string, error) {
	return f(ctx, lastOutput)
}

// runnerFunc executes a single advisor invocation and returns its combined
// stdout. It MIRRORS engine.runnerFunc (command_engine.go) exactly so the
// subprocess is injectable: production uses the real `claude -p` runner (behind
// the realclaude build tag), while the default tests pass a fake that returns
// fixture stdout without spawning the CLI.
//
// argv is the resolved advisor command, dir the working directory, stdin the
// prompt/context payload, and env the merged environment. The returned error is
// the raw process error; the parser classifies the stdout.
type runnerFunc func(ctx context.Context, argv []string, dir string, stdin []byte, env []string) (stdout []byte, err error)

// Advisor parse / invocation errors. They wrap a shared root so a caller can
// errors.Is against ErrAdvisor while still distinguishing the specific cause.
var (
	// ErrAdvisor is the root advisor error all advisor failures wrap.
	ErrAdvisor = errors.New("sentinel: advisor")
	// ErrNoAdvice means the advisor produced no parseable verdict object at all
	// (empty/timed-out/prose-only output).
	ErrNoAdvice = fmt.Errorf("%w: no advice in output", ErrAdvisor)
	// ErrMalformedAdvice means a candidate object was found but its verdict value
	// is missing or out of the 3-valued enum. It is NEVER coerced into a verdict.
	ErrMalformedAdvice = fmt.Errorf("%w: malformed advice", ErrAdvisor)
)

// adviceKey is the JSON key the advisor verdict object carries. It mirrors the
// engine parser's "result" selector but is distinct ("advice") so the sentinel's
// 3-valued liveness verdict is never confused with a develop/verify Verdict.
const adviceKey = "advice"

// ParseAdvice extracts the gray-zone verdict from advisor stdout that may be
// wrapped in prose or markdown (mirroring engine.ParseVerdict / lastResultObject
// / balancedObjects). It scans every balanced top-level {...} span and selects
// the LAST one that both decodes as JSON and carries a valid 3-valued "advice"
// key, returning that advice plus the optional "reason" string.
//
// It NEVER fakes advice: no qualifying object -> ErrNoAdvice; an object whose
// advice is missing or out of the enum -> ErrMalformedAdvice. The last-valid
// rule lets the advisor narrate, then emit the final verdict object last.
func ParseAdvice(stdout []byte) (Advice, string, error) {
	type adviceObj struct {
		Advice string `json:"advice"`
		Reason string `json:"reason"`
	}
	var (
		last      *adviceObj
		sawAdvice bool
	)
	for _, span := range balancedObjects(stdout) {
		var probe map[string]json.RawMessage
		if err := json.Unmarshal(span, &probe); err != nil {
			continue // not a valid JSON object
		}
		if _, ok := probe[adviceKey]; !ok {
			continue // not an advice object
		}
		sawAdvice = true
		var obj adviceObj
		if err := json.Unmarshal(span, &obj); err != nil {
			continue // advice-keyed but undecodable; keep scanning for a clean one
		}
		cp := obj
		last = &cp
	}
	if last == nil {
		if sawAdvice {
			// An advice-keyed object existed but never decoded cleanly: malformed.
			return "", "", ErrMalformedAdvice
		}
		return "", "", ErrNoAdvice
	}
	adv := Advice(strings.TrimSpace(last.Advice))
	if !adv.Valid() {
		return "", "", fmt.Errorf("%w: out-of-enum advice %q", ErrMalformedAdvice, last.Advice)
	}
	return adv, strings.TrimSpace(last.Reason), nil
}

// balancedObjects returns every balanced top-level {...} byte span in data, in
// order, tracking string/escape state so braces inside JSON strings do not
// corrupt nesting. It is a deliberate MIRROR of engine.balancedObjects (the seam
// is duplicated rather than imported because the engine's copy is unexported and
// the engine package is frozen — additive-only per ADR-0021).
func balancedObjects(data []byte) [][]byte {
	var spans [][]byte
	depth := 0
	start := -1
	inString := false
	escaped := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 && start >= 0 {
					spans = append(spans, data[start:i+1])
					start = -1
				}
			}
		}
	}
	return spans
}

// CommandAdvisor is the concrete subprocess-backed Advisor: it runs an advisor
// command (a tight `claude -p` invocation in production) via an injected runner
// and parses its stdout with ParseAdvice. It holds NO LLM logic itself — the
// judgement lives in the performer process — and never fabricates advice (a parse
// failure or process error surfaces as an error that Assess treats conservatively).
//
// The runner is the test seam (mirroring CommandEngine): default tests inject a
// fake that returns fixture stdout, so the gate stays offline. The real `claude
// -p` runner lives behind the `realclaude` build tag (advisor_realclaude.go) and
// is only wired in when CP_REAL_CLAUDE=1.
type CommandAdvisor struct {
	argv []string
	dir  string
	env  []string
	run  runnerFunc
}

// NewCommandAdvisor builds a CommandAdvisor that runs argv in dir with extra env,
// executed by run. A nil run is a configuration error caught at Advise time
// (returned as an advisor error) rather than a panic, so a misconfigured advisor
// degrades to the conservative Continue rather than crashing the watchdog.
func NewCommandAdvisor(argv []string, dir string, env []string, run runnerFunc) *CommandAdvisor {
	return &CommandAdvisor{argv: argv, dir: dir, env: env, run: run}
}

// Advise builds the advisor prompt from lastOutput, runs the command, and parses
// the verdict. Any process error or unparseable output is returned as an advisor
// error (Assess maps it to the conservative Continue). It never returns a verdict
// it did not actually parse from the advisor's output.
func (a *CommandAdvisor) Advise(ctx context.Context, lastOutput string) (Advice, string, error) {
	if a == nil || a.run == nil {
		return "", "", fmt.Errorf("%w: no runner configured", ErrAdvisor)
	}
	if len(a.argv) == 0 {
		return "", "", fmt.Errorf("%w: empty advisor command", ErrAdvisor)
	}
	stdin := []byte(AdvisorPrompt(lastOutput))
	stdout, err := a.run(ctx, a.argv, a.dir, stdin, a.env)
	if err != nil {
		// A process error with usable output is still worth parsing (e.g. exit 1 but
		// a verdict on stdout); only a truly empty error result is a hard failure.
		if len(strings.TrimSpace(string(stdout))) == 0 {
			return "", "", fmt.Errorf("%w: run: %v", ErrAdvisor, err)
		}
	}
	return ParseAdvice(stdout)
}

// AdvisorPrompt builds the narrow gray-zone prompt fed to the advisor: it states
// the role (liveness advisor, NOT a quality judge), the 3-valued output contract,
// and embeds the stalled run's recent output. It demands the verdict as the LAST
// single-line JSON object so ParseAdvice's last-valid rule selects it — exactly
// mirroring the engine's verdict-prompt convention (realclaude_smoke_test.go).
func AdvisorPrompt(lastOutput string) string {
	var b strings.Builder
	b.WriteString("You are a LIVENESS ADVISOR for an automated build run. You do NOT judge code quality or correctness. ")
	b.WriteString("A run appears stalled (no output for a while) but its process is still alive. ")
	b.WriteString("Look ONLY at the recent output below and decide whether the run is still making progress, is wedged, or needs a human.\n\n")
	b.WriteString("Recent output (tail):\n")
	b.WriteString("<<<\n")
	b.WriteString(lastOutput)
	b.WriteString("\n>>>\n\n")
	b.WriteString("As the VERY LAST thing you output, print ONE single-line JSON object and NOTHING after it, in exactly this schema:\n")
	b.WriteString(`{"advice":"<progressing|stuck|needs_human>","reason":"<one short sentence>"}` + "\n")
	b.WriteString(`The "advice" value MUST be exactly one of: progressing (still working), stuck (wedged, will not finish), needs_human (blocked on something only a human can resolve).`)
	return b.String()
}
