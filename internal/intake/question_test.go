package intake

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// validQuestionsOutput wraps a valid clarifying-questions block in narration +
// fences, the way the real model is prompted to in clarifying mode.
const validQuestionsOutput = `The request is ambiguous; I need to clarify before distilling.

` + questionsFenceStart + `
questions:
  - question: "Which auth method should checkout use?"
    header: "Auth method"
    multi_select: false
    options:
      - label: "OAuth"
        description: "Delegate to an external identity provider (Google/GitHub)."
      - label: "Email + password"
        description: "Store hashed credentials in our own users table."
` + questionsFenceEnd + `

Pick one and I will distill the scenario.`

// validQuestion returns a shape-valid single question for mutation in table tests.
func validQuestion() Question {
	return Question{
		Question: "Which auth method?",
		Header:   "Auth",
		Options: []QuestionOption{
			{Label: "OAuth", Description: "External IdP."},
			{Label: "Password", Description: "Own users table."},
		},
	}
}

func TestValidateQuestions(t *testing.T) {
	// fiveOptions builds an option-overflow question.
	fiveOptions := validQuestion()
	fiveOptions.Options = []QuestionOption{
		{Label: "a", Description: "d"}, {Label: "b", Description: "d"},
		{Label: "c", Description: "d"}, {Label: "e", Description: "d"},
		{Label: "f", Description: "d"},
	}

	dupLabel := validQuestion()
	dupLabel.Options = []QuestionOption{
		{Label: "OAuth", Description: "one"},
		{Label: "OAuth", Description: "two"},
	}

	fourDistinct := func() []Question {
		qs := make([]Question, 4)
		for i := range qs {
			q := validQuestion()
			q.Question = "Question number " + string(rune('A'+i)) + "?"
			qs[i] = q
		}
		return qs
	}()

	tests := []struct {
		name    string
		qs      []Question
		wantErr bool
	}{
		{"valid single", []Question{validQuestion()}, false},
		{"valid four distinct", fourDistinct, false},
		{"empty set", nil, true},
		{"too many (5)", append(fourDistinct, validQuestion()), true},
		{"missing question text", []Question{func() Question { q := validQuestion(); q.Question = "  "; return q }()}, true},
		{"missing header", []Question{func() Question { q := validQuestion(); q.Header = ""; return q }()}, true},
		{"header too long", []Question{func() Question { q := validQuestion(); q.Header = "ThisHeaderIsWayTooLong"; return q }()}, true},
		{"too few options (1)", []Question{func() Question { q := validQuestion(); q.Options = q.Options[:1]; return q }()}, true},
		{"too many options (5)", []Question{fiveOptions}, true},
		{"empty option label", []Question{func() Question { q := validQuestion(); q.Options[0].Label = ""; return q }()}, true},
		{"empty option description", []Question{func() Question { q := validQuestion(); q.Options[0].Description = " "; return q }()}, true},
		{"duplicate option label", []Question{dupLabel}, true},
		{"duplicate question text", []Question{validQuestion(), validQuestion()}, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateQuestions(tc.qs)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected nil, got: %v", err)
			}
		})
	}
}

func TestValidateQuestions_HeaderBoundaryExactly12(t *testing.T) {
	q := validQuestion()
	q.Header = "123456789012" // exactly 12 chars → allowed
	if err := ValidateQuestions([]Question{q}); err != nil {
		t.Fatalf("12-char header must be allowed: %v", err)
	}
}

func TestParseQuestions_ValidBlock(t *testing.T) {
	got, err := ParseQuestions([]byte(validQuestionsOutput))
	if err != nil {
		t.Fatalf("parse valid: %v", err)
	}
	if len(got) != 1 || got[0].Header != "Auth method" || len(got[0].Options) != 2 {
		t.Fatalf("unexpected questions: %+v", got)
	}
}

func TestParseQuestions_NoBlock_IsNoQuestions(t *testing.T) {
	_, err := ParseQuestions([]byte("just prose, no fenced block here"))
	if !errors.Is(err, ErrNoQuestions) {
		t.Fatalf("expected ErrNoQuestions, got: %v", err)
	}
}

func TestParseQuestions_EmptyList_IsNoQuestions(t *testing.T) {
	out := questionsFenceStart + "\nquestions: []\n" + questionsFenceEnd
	_, err := ParseQuestions([]byte(out))
	if !errors.Is(err, ErrNoQuestions) {
		t.Fatalf("expected ErrNoQuestions for empty list, got: %v", err)
	}
}

func TestParseQuestions_BadYAML_IsMalformed(t *testing.T) {
	out := questionsFenceStart + "\nquestions: [this : is : not valid\n" + questionsFenceEnd
	_, err := ParseQuestions([]byte(out))
	if !errors.Is(err, ErrMalformedQuestions) {
		t.Fatalf("expected ErrMalformedQuestions, got: %v", err)
	}
}

func TestParseQuestions_InvalidSet_IsMalformed(t *testing.T) {
	// Well-formed YAML, but only one option (fails ValidateQuestions).
	out := questionsFenceStart + `
questions:
  - question: "pick"
    header: "h"
    options:
      - label: "only"
        description: "one option is too few"
` + questionsFenceEnd
	_, err := ParseQuestions([]byte(out))
	if !errors.Is(err, ErrMalformedQuestions) {
		t.Fatalf("expected ErrMalformedQuestions for invalid set, got: %v", err)
	}
	if !strings.Contains(err.Error(), "too few options") {
		t.Fatalf("error should surface the validation reason: %v", err)
	}
}

func TestParseOutcome_ScenariosWin(t *testing.T) {
	out, err := ParseOutcome([]byte(validDistillerOutput))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(out.Scenarios) != 1 || len(out.Questions) != 0 {
		t.Fatalf("expected scenarios-only outcome, got: %+v", out)
	}
}

func TestParseOutcome_Questions(t *testing.T) {
	out, err := ParseOutcome([]byte(validQuestionsOutput))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(out.Questions) != 1 || len(out.Scenarios) != 0 {
		t.Fatalf("expected questions-only outcome, got: %+v", out)
	}
}

func TestParseOutcome_BothBlocks_PreferScenarios(t *testing.T) {
	// A questions block AND a scenarios block: the concrete answer wins.
	out, err := ParseOutcome([]byte(validQuestionsOutput + "\n" + validDistillerOutput))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(out.Scenarios) != 1 || len(out.Questions) != 0 {
		t.Fatalf("scenarios should win when both present, got: %+v", out)
	}
}

func TestParseOutcome_MalformedScenariosSurfaces(t *testing.T) {
	// A malformed scenarios block is surfaced even when valid questions follow:
	// the model tried to answer and produced a broken block (never hide it).
	malformed := scenariosFenceStart + "\nscenarios: [bad : yaml : here\n" + scenariosFenceEnd
	_, err := ParseOutcome([]byte(malformed + "\n" + validQuestionsOutput))
	if !errors.Is(err, ErrMalformedScenarios) {
		t.Fatalf("expected ErrMalformedScenarios, got: %v", err)
	}
}

func TestParseOutcome_Neither_IsNoScenarios(t *testing.T) {
	// Neither block → the existing never-fake-green sentinel (gateway 422 unchanged).
	_, err := ParseOutcome([]byte("everything looks great, nothing to clarify"))
	if !errors.Is(err, ErrNoScenarios) {
		t.Fatalf("expected ErrNoScenarios for no blocks, got: %v", err)
	}
}

func TestDistillOrClarify_Questions(t *testing.T) {
	d := NewClarifyingDistillerWithRunner(stubRunner(validQuestionsOutput, nil))
	out, err := d.DistillOrClarify(context.Background(), "build me something, not sure what")
	if err != nil {
		t.Fatalf("clarify: %v", err)
	}
	if len(out.Questions) != 1 || len(out.Scenarios) != 0 {
		t.Fatalf("expected a clarifying-question outcome, got: %+v", out)
	}
}

func TestDistillOrClarify_Scenarios(t *testing.T) {
	d := NewClarifyingDistillerWithRunner(stubRunner(validDistillerOutput, nil))
	out, err := d.DistillOrClarify(context.Background(), "we need an in-memory state store")
	if err != nil {
		t.Fatalf("clarify: %v", err)
	}
	if len(out.Scenarios) != 1 || len(out.Questions) != 0 {
		t.Fatalf("expected a scenarios outcome, got: %+v", out)
	}
}

func TestDistillOrClarify_NoRunner_Rejected(t *testing.T) {
	// A distiller with NEITHER runner configured cannot run at all.
	d := &CommandDistiller{}
	if _, err := d.DistillOrClarify(context.Background(), "x"); err == nil {
		t.Fatal("expected error when no runner is configured")
	}
}

func TestDistillOrClarify_FallsBackToDistillRunner(t *testing.T) {
	// No clarify runner but a distill runner: DistillOrClarify degrades to the
	// frozen scenarios-only behavior (it never emits questions).
	d := NewCommandDistillerWithRunner(stubRunner(validDistillerOutput, nil))
	out, err := d.DistillOrClarify(context.Background(), "we need an in-memory state store")
	if err != nil {
		t.Fatalf("clarify fallback: %v", err)
	}
	if len(out.Scenarios) != 1 || len(out.Questions) != 0 {
		t.Fatalf("expected scenarios via distill-runner fallback, got: %+v", out)
	}
}

func TestDistillOrClarify_EmptyConversation_Rejected(t *testing.T) {
	d := NewClarifyingDistillerWithRunner(stubRunner(validQuestionsOutput, nil))
	if _, err := d.DistillOrClarify(context.Background(), "   "); err == nil {
		t.Fatal("expected error for empty conversation")
	}
}

func TestDistillOrClarify_RunErrorEmptyOutput_NoScenarios(t *testing.T) {
	d := NewClarifyingDistillerWithRunner(stubRunner("", errors.New("boom")))
	if _, err := d.DistillOrClarify(context.Background(), "conversation"); !errors.Is(err, ErrNoScenarios) {
		t.Fatalf("expected ErrNoScenarios on run error + empty output, got: %v", err)
	}
}

func TestDistillOrClarify_RunErrorWithOutput_StillParses(t *testing.T) {
	d := NewClarifyingDistillerWithRunner(stubRunner(validQuestionsOutput, errors.New("exit 1")))
	out, err := d.DistillOrClarify(context.Background(), "conversation")
	if err != nil {
		t.Fatalf("clarify with usable output despite run error: %v", err)
	}
	if len(out.Questions) != 1 {
		t.Fatalf("expected 1 question, got: %+v", out)
	}
}

// stubStreamRunner replays crafted lines through onLine, then returns crafted full
// output — exercising the streaming seam fully offline (no real LLM).
func stubStreamRunner(lines []string, full string, err error) streamRunner {
	return func(_ context.Context, _ string, onLine func(string)) ([]byte, error) {
		for _, l := range lines {
			if onLine != nil {
				onLine(l)
			}
		}
		return []byte(full), err
	}
}

func TestDistillOrClarifyStream_Questions_EmitsLines(t *testing.T) {
	lines := []string{"thinking...", "let me ask", "<<<QUESTIONS"}
	var got []string
	d := NewStreamingDistillerWithRunner(stubStreamRunner(lines, validQuestionsOutput, nil))
	out, err := d.DistillOrClarifyStream(context.Background(), "vague request", func(l string) {
		got = append(got, l)
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(out.Questions) != 1 || len(out.Scenarios) != 0 {
		t.Fatalf("expected a questions outcome, got: %+v", out)
	}
	if len(got) != len(lines) {
		t.Fatalf("onLine called %d times, want %d", len(got), len(lines))
	}
}

func TestDistillOrClarifyStream_Scenarios(t *testing.T) {
	d := NewStreamingDistillerWithRunner(stubStreamRunner([]string{"x"}, validDistillerOutput, nil))
	out, err := d.DistillOrClarifyStream(context.Background(), "an in-memory state store", nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(out.Scenarios) != 1 || len(out.Questions) != 0 {
		t.Fatalf("expected a scenarios outcome, got: %+v", out)
	}
}

func TestDistillOrClarifyStream_NilOnLine_Safe(t *testing.T) {
	d := NewStreamingDistillerWithRunner(stubStreamRunner([]string{"a", "b"}, validQuestionsOutput, nil))
	if _, err := d.DistillOrClarifyStream(context.Background(), "x", nil); err != nil {
		t.Fatalf("nil onLine must be safe: %v", err)
	}
}

func TestDistillOrClarifyStream_DegradesWithoutStreamRunner(t *testing.T) {
	// No stream runner, but a clarify runner: stream degrades to DistillOrClarify.
	d := NewClarifyingDistillerWithRunner(stubRunner(validQuestionsOutput, nil))
	out, err := d.DistillOrClarifyStream(context.Background(), "x", func(string) {
		t.Fatal("onLine must not be called when there is no stream runner")
	})
	if err != nil {
		t.Fatalf("degrade: %v", err)
	}
	if len(out.Questions) != 1 {
		t.Fatalf("expected questions via non-streaming fallback, got: %+v", out)
	}
}

func TestDistillOrClarifyStream_EmptyConversation_Rejected(t *testing.T) {
	d := NewStreamingDistillerWithRunner(stubStreamRunner(nil, validQuestionsOutput, nil))
	if _, err := d.DistillOrClarifyStream(context.Background(), "  ", nil); err == nil {
		t.Fatal("expected error for empty conversation")
	}
}

func TestDistillOrClarifyStream_RunErrorEmptyOutput_NoScenarios(t *testing.T) {
	d := NewStreamingDistillerWithRunner(stubStreamRunner(nil, "", errors.New("boom")))
	if _, err := d.DistillOrClarifyStream(context.Background(), "x", nil); !errors.Is(err, ErrNoScenarios) {
		t.Fatalf("expected ErrNoScenarios on run error + empty output, got: %v", err)
	}
}
