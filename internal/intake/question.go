package intake

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Question is the intake-layer mirror of Claude Code's AskUserQuestion schema
// (ADR-0047): when the conversation is too ambiguous to distill into concrete
// scenarios, the distiller asks the director a SPECIFIC, structured multiple-choice
// clarifying question instead of guessing. This is the "ask, don't assume" half of
// assisted intake — the deterministic, gate-testable shape of an LLM's clarifying
// turn. The UI renders it as a chip-headed card with selectable options and adds an
// "Other" free-text choice automatically (so the model must NOT emit one).
type Question struct {
	// Question is the full question text shown to the director. Required.
	Question string `yaml:"question"`
	// Header is a short chip label (≤12 chars, CC contract). Required.
	Header string `yaml:"header"`
	// Options are the offered choices (2-4, CC contract). Required.
	Options []QuestionOption `yaml:"options"`
	// MultiSelect lets the director pick more than one option when true.
	MultiSelect bool `yaml:"multi_select"`
}

// QuestionOption is one offered choice within a Question (CC AskUserQuestion
// option). Label is the short choice (CC guidance: 1-5 words, not hard-enforced
// here); Description explains what choosing it means and is required. A preview
// field exists in CC but is deferred — not carried yet.
type QuestionOption struct {
	// Label is the short choice text. Required, unique within the question.
	Label string `yaml:"label"`
	// Description explains the choice. Required (CC requires it).
	Description string `yaml:"description"`
}

// DistillOutcome is the rich result of a clarifying distill run: EITHER a set of
// concrete scenarios (the conversation was specific enough) OR a set of clarifying
// questions (it was ambiguous and the model asked instead of guessing). Exactly one
// of the two is populated on success; both empty is impossible (ParseOutcome maps a
// model that produced neither to ErrNoScenarios, the never-fake-green sentinel).
type DistillOutcome struct {
	// Scenarios is non-empty when the model distilled a concrete answer.
	Scenarios []Scenario
	// Questions is non-empty when the model asked for clarification instead.
	Questions []Question
	// Holdout is the OPTIONAL auto-generated hidden-holdout test (Faz-S S4): worktree-relative path
	// -> file content. Populated only when the model emitted a <<<HOLDOUT>>> block alongside the
	// scenarios; nil otherwise. The director REVIEWS it before approving (the human is the guard,
	// since the same model authored the test + will author the code — ADR-0018 note). Never logged.
	Holdout map[string]string
}

// ClarifyingDistiller is the ADDITIVE distiller seam (ADR-0047) that may return
// clarifying questions instead of scenarios. It sits ALONGSIDE the frozen
// Distiller.Distill contract (which is untouched): existing callers/fakes that only
// know Distill keep working, while the gateway type-asserts to this richer seam when
// the wired distiller supports it. DistillOrClarify MUST NOT fabricate: a model that
// produces neither valid scenarios nor valid questions yields ErrNoScenarios.
type ClarifyingDistiller interface {
	// DistillOrClarify converts a conversation into either validated scenarios or
	// validated clarifying questions.
	DistillOrClarify(ctx context.Context, conversation string) (DistillOutcome, error)
}

// StreamingDistiller is the additive STREAMING seam (ADR-0047, Q3c.4): same outcome as
// DistillOrClarify, but it invokes onLine for each line of model output as it is
// produced so callers can surface live progress during a long model call. It sits
// ALONGSIDE the other seams (all untouched). The line content is for coarse progress
// only — callers MUST NOT forward it to an untrusted client (it may echo the
// conversation); the gateway streams line COUNTS, never the lines.
type StreamingDistiller interface {
	// DistillOrClarifyStream is DistillOrClarify with per-line progress via onLine
	// (which may be nil). The parsed outcome is identical to DistillOrClarify.
	DistillOrClarifyStream(ctx context.Context, conversation string, onLine func(line string)) (DistillOutcome, error)
}

// ErrNoQuestions means the model output contained no parseable questions block. It
// is internal to ParseOutcome's fallthrough (a missing questions block is not by
// itself an error when scenarios were the intent); callers see ErrNoScenarios when
// the model produced nothing usable at all.
var ErrNoQuestions = errors.New("intake: distiller produced no questions")

// ErrMalformedQuestions means a questions block was found but did not parse or did
// not validate. It wraps the underlying decode/validation detail and maps to a 422
// at the gateway, exactly like ErrMalformedScenarios.
var ErrMalformedQuestions = errors.New("intake: distiller produced malformed questions")

// Question-shape limits, mirroring the CC AskUserQuestion JSON Schema bounds.
const (
	maxQuestions   = 4  // questions: maxItems
	minOptions     = 2  // options: minItems
	maxOptions     = 4  // options: maxItems
	maxHeaderChars = 12 // header: "max 12 chars" chip label
)

// questionsFenceStart and questionsFenceEnd delimit the machine-readable questions
// block in the model's output, mirroring the scenarios fences so the same
// last-block extraction discipline applies.
const (
	questionsFenceStart = "<<<QUESTIONS"
	questionsFenceEnd   = "QUESTIONS>>>"
)

// questionBlock is the YAML wrapper the model emits for a clarifying turn: a
// top-level `questions:` list (the analogue of scenarioBlock).
type questionBlock struct {
	Questions []Question `yaml:"questions"`
}

// holdoutFenceStart/End delimit the OPTIONAL auto-generated hidden-holdout block (Faz-S S4) the
// model may emit ALONGSIDE the scenarios. Mirrors the scenarios/questions fences (last-block rule).
const (
	holdoutFenceStart = "<<<HOLDOUT"
	holdoutFenceEnd   = "HOLDOUT>>>"
)

// holdoutBlock is the YAML wrapper for the auto-generated holdout: worktree-relative path -> file
// content (the hidden test that proves the scenario's acceptance).
type holdoutBlock struct {
	Files map[string]string `yaml:"files"`
}

// ParseHoldout extracts the OPTIONAL <<<HOLDOUT>>> block (Faz-S S4). Returns (files, true) for a
// well-formed non-empty block; (nil, false) when absent OR malformed — the holdout is optional, so
// a parse failure is NOT an error (the scenario still distills; the director just gets no
// auto-holdout to review). Reuses the package's extractFenced (last-block rule). Never logs content.
func ParseHoldout(stdout []byte) (map[string]string, bool) {
	block, ok := extractFenced(string(stdout), holdoutFenceStart, holdoutFenceEnd)
	if !ok {
		return nil, false
	}
	var hb holdoutBlock
	if err := yaml.Unmarshal([]byte(block), &hb); err != nil || len(hb.Files) == 0 {
		return nil, false
	}
	// Drop any blank-path / blank-content entries so a sloppy block can't yield an empty file.
	files := make(map[string]string, len(hb.Files))
	for path, content := range hb.Files {
		if strings.TrimSpace(path) != "" && content != "" {
			files[path] = content
		}
	}
	if len(files) == 0 {
		return nil, false
	}
	return files, true
}

// QuestionValidationError aggregates why a question set was rejected. Index is the
// 0-based offending question, or -1 for a set-level problem (count). It mirrors
// ValidationError's all-reasons-at-once style so the operator sees every problem.
type QuestionValidationError struct {
	// Index is the 0-based question index, or -1 for a set-level rejection.
	Index int
	// Header is the offending question's header, when known.
	Header string
	// Reasons is the non-empty list of human-readable rejection reasons.
	Reasons []string
}

// Error renders the rejection at a stable, secret-free location string.
func (e *QuestionValidationError) Error() string {
	loc := "questions"
	if e.Index >= 0 {
		if strings.TrimSpace(e.Header) != "" {
			loc = fmt.Sprintf("question %d (%q)", e.Index, e.Header)
		} else {
			loc = fmt.Sprintf("question %d", e.Index)
		}
	}
	return fmt.Sprintf("%s invalid: %s", loc, strings.Join(e.Reasons, "; "))
}

// ValidateQuestions checks a clarifying-question set against the CC AskUserQuestion
// contract (ADR-0047). It is deterministic and rejects:
//
//   - an empty set, or more than maxQuestions;
//   - a question with no text or no header, or a header longer than maxHeaderChars;
//   - fewer than minOptions or more than maxOptions options;
//   - an option with an empty label or empty description;
//   - duplicate option labels within a question;
//   - duplicate question texts across the set.
//
// The "1-5 word" label guidance from CC is intentionally NOT hard-enforced (word
// counts are brittle for model output and not a UI-correctness invariant); the chip
// length and option bounds, which the UI contract depends on, are enforced. Returns
// nil for a well-formed set, else a *QuestionValidationError.
func ValidateQuestions(qs []Question) error {
	if len(qs) == 0 {
		return &QuestionValidationError{Index: -1, Reasons: []string{"no questions"}}
	}
	if len(qs) > maxQuestions {
		return &QuestionValidationError{Index: -1, Reasons: []string{fmt.Sprintf("too many questions (%d; max %d)", len(qs), maxQuestions)}}
	}

	seenQ := make(map[string]struct{}, len(qs))
	for i := range qs {
		q := qs[i]
		var reasons []string

		qt := strings.TrimSpace(q.Question)
		if qt == "" {
			reasons = append(reasons, "missing question text")
		}

		h := strings.TrimSpace(q.Header)
		switch {
		case h == "":
			reasons = append(reasons, "missing header")
		case utf8.RuneCountInString(h) > maxHeaderChars:
			reasons = append(reasons, fmt.Sprintf("header %q exceeds %d chars", h, maxHeaderChars))
		}

		switch {
		case len(q.Options) < minOptions:
			reasons = append(reasons, fmt.Sprintf("too few options (%d; min %d)", len(q.Options), minOptions))
		case len(q.Options) > maxOptions:
			reasons = append(reasons, fmt.Sprintf("too many options (%d; max %d)", len(q.Options), maxOptions))
		}

		seenLabel := make(map[string]struct{}, len(q.Options))
		for _, opt := range q.Options {
			l := strings.TrimSpace(opt.Label)
			if l == "" {
				reasons = append(reasons, "option with empty label")
				continue
			}
			if strings.TrimSpace(opt.Description) == "" {
				reasons = append(reasons, fmt.Sprintf("option %q has no description", l))
			}
			if _, dup := seenLabel[l]; dup {
				reasons = append(reasons, fmt.Sprintf("duplicate option label %q", l))
			}
			seenLabel[l] = struct{}{}
		}

		if len(reasons) > 0 {
			return &QuestionValidationError{Index: i, Header: q.Header, Reasons: reasons}
		}

		// Shape is valid; enforce set-level question-text uniqueness.
		if _, dup := seenQ[qt]; dup {
			return &QuestionValidationError{Index: i, Header: q.Header, Reasons: []string{"duplicate question text in set"}}
		}
		seenQ[qt] = struct{}{}
	}
	return nil
}

// ParseQuestions is the deterministic parser for a clarifying turn: it lifts the
// fenced questions block out of prose, decodes it as a `questions:` list, and
// validates the set. It NEVER coerces unusable output into questions:
//
//   - no fenced block, or an empty list -> ErrNoQuestions;
//   - a block that fails to YAML-decode -> ErrMalformedQuestions;
//   - a set that fails ValidateQuestions -> ErrMalformedQuestions (wraps the reason).
//
// On success it returns the validated questions in output order.
func ParseQuestions(stdout []byte) ([]Question, error) {
	block, ok := extractFenced(string(stdout), questionsFenceStart, questionsFenceEnd)
	if !ok {
		return nil, fmt.Errorf("%w: no %s..%s block in output", ErrNoQuestions, questionsFenceStart, questionsFenceEnd)
	}
	var parsed questionBlock
	if err := yaml.Unmarshal([]byte(block), &parsed); err != nil {
		return nil, fmt.Errorf("%w: decode block: %v", ErrMalformedQuestions, err)
	}
	if len(parsed.Questions) == 0 {
		return nil, fmt.Errorf("%w: block has an empty questions list", ErrNoQuestions)
	}
	if err := ValidateQuestions(parsed.Questions); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformedQuestions, err)
	}
	return parsed.Questions, nil
}

// ParseOutcome is the deterministic parser for a clarifying distill run: the model
// emits EITHER a scenarios block OR a questions block, and this returns whichever it
// finds as a DistillOutcome. Precedence: a concrete scenarios answer is preferred
// over questions when both are present (the model resolved the ambiguity). Honesty
// rules (never-fake-green):
//
//   - a valid scenarios block            -> {Scenarios}, nil
//   - a malformed scenarios block         -> ErrMalformedScenarios (surfaced, not hidden)
//   - no scenarios but valid questions    -> {Questions}, nil
//   - no scenarios but malformed questions-> ErrMalformedQuestions
//   - neither block                       -> ErrNoScenarios (the existing 422 sentinel)
//
// Mapping the "neither" case to ErrNoScenarios keeps the gateway's existing 422
// behavior byte-identical when no clarification is offered.
func ParseOutcome(stdout []byte) (DistillOutcome, error) {
	scenarios, scErr := ParseScenarios(stdout)
	if scErr == nil {
		// Faz-S S4: attach the OPTIONAL auto-generated holdout (best-effort; nil when absent or
		// malformed — the scenario still distills, the human reviews the holdout if present).
		holdout, _ := ParseHoldout(stdout)
		return DistillOutcome{Scenarios: scenarios, Holdout: holdout}, nil
	}
	if errors.Is(scErr, ErrMalformedScenarios) {
		return DistillOutcome{}, scErr
	}

	// scErr is ErrNoScenarios (no scenarios block): try a clarifying turn.
	questions, qErr := ParseQuestions(stdout)
	if qErr == nil {
		return DistillOutcome{Questions: questions}, nil
	}
	if errors.Is(qErr, ErrMalformedQuestions) {
		return DistillOutcome{}, qErr
	}

	// Neither a scenarios nor a questions block: nothing usable. Preserve the
	// never-fake-green sentinel the gateway already maps to 422.
	return DistillOutcome{}, fmt.Errorf("%w: no scenarios or questions block in output", ErrNoScenarios)
}
