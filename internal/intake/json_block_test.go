package intake

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// The clarifying distiller now emits JSON inside the <<<SCENARIOS>>> / <<<QUESTIONS>>>
// fences instead of hand-written YAML, because free-text (Turkish with apostrophes,
// colons, or quotes) routinely broke a YAML scalar — the real "decode block: yaml:
// line N: could not find expected ':'" failure a director hit. The parsers use
// yaml.Unmarshal, which parses JSON (valid YAML 1.2 flow), so the fix is wire-format
// only. These tests lock that contract end to end + the markdown-fence defense.

func TestParseQuestions_JSONBlock_TurkishFreeText(t *testing.T) {
	// Free text carries BOTH an apostrophe ('Takip ID') and a colon (trackingId:) —
	// the exact characters that broke the YAML path.
	out := "let me ask the director\n" + questionsFenceStart + "\n" +
		`{"questions":[{"question":"Araç 'Takip ID' alanının kaderi ne olmalı?","header":"Takip ID","multi_select":false,` +
		`"options":[{"label":"Kaldır","description":"trackingId: tüm referanslarıyla kaldırılsın"},` +
		`{"label":"Koru","description":"alan kalsın"}]}]}` + "\n" + questionsFenceEnd
	qs, err := ParseQuestions([]byte(out))
	if err != nil {
		t.Fatalf("ParseQuestions(JSON) error = %v", err)
	}
	if len(qs) != 1 || qs[0].Header != "Takip ID" || len(qs[0].Options) != 2 {
		t.Fatalf("unexpected parse: %+v", qs)
	}
	if !strings.Contains(qs[0].Question, "'Takip ID'") {
		t.Fatalf("apostrophe-bearing question lost: %q", qs[0].Question)
	}
	if !strings.Contains(qs[0].Options[0].Description, "trackingId: tüm") {
		t.Fatalf("colon-bearing description lost: %q", qs[0].Options[0].Description)
	}
}

func TestParseQuestions_JSONWrappedInMarkdownFence(t *testing.T) {
	// A model that ignores "no markdown fences" and wraps its JSON in ```json must
	// still parse, thanks to stripCodeFence.
	out := questionsFenceStart + "\n```json\n" +
		`{"questions":[{"question":"Pick one?","header":"Pick","multi_select":false,` +
		`"options":[{"label":"A","description":"first choice"},{"label":"B","description":"second choice"}]}]}` +
		"\n```\n" + questionsFenceEnd
	qs, err := ParseQuestions([]byte(out))
	if err != nil {
		t.Fatalf("ParseQuestions(JSON-in-markdown-fence) error = %v", err)
	}
	if len(qs) != 1 || qs[0].Question != "Pick one?" {
		t.Fatalf("unexpected parse: %+v", qs)
	}
}

func TestParseScenarios_JSONBlock_TurkishFreeText(t *testing.T) {
	out := "draft thinking\n" + scenariosFenceStart + "\n" +
		`{"scenarios":[{"id":"RM-1","title":"Servis Şirketi: tümüyle kaldır","lane":"general","tier":"T2",` +
		`"deps":[],"acceptance":["migration up/down 'yeşil' olmalı","driver list serviceCompanyId'siz çalışmalı"],` +
		`"hidden_holdout_ref":"pg://holdouts/RM-1"}]}` + "\n" + scenariosFenceEnd
	sc, err := ParseScenarios([]byte(out))
	if err != nil {
		t.Fatalf("ParseScenarios(JSON) error = %v", err)
	}
	if len(sc) != 1 || sc[0].ID != "RM-1" || !strings.Contains(sc[0].Title, "Servis Şirketi:") {
		t.Fatalf("unexpected parse: %+v", sc)
	}
	if len(sc[0].Acceptance) != 2 || !strings.Contains(sc[0].Acceptance[0], "'yeşil'") {
		t.Fatalf("acceptance with quotes/colons lost: %+v", sc[0].Acceptance)
	}
}

func TestParseOutcome_JSONQuestionsAndScenarios(t *testing.T) {
	qOut := questionsFenceStart + "\n" +
		`{"questions":[{"question":"A:B or C?","header":"Pick","multi_select":false,` +
		`"options":[{"label":"X","description":"d1"},{"label":"Y","description":"d2"}]}]}` +
		"\n" + questionsFenceEnd
	oc, err := ParseOutcome([]byte(qOut))
	if err != nil {
		t.Fatalf("ParseOutcome(questions) error = %v", err)
	}
	if len(oc.Questions) != 1 || len(oc.Scenarios) != 0 {
		t.Fatalf("want questions outcome, got %+v", oc)
	}

	sOut := scenariosFenceStart + "\n" +
		`{"scenarios":[{"id":"S-1","title":"do: a thing","lane":"web","tier":"T3",` +
		`"deps":[],"acceptance":["it works"],"hidden_holdout_ref":"pg://holdouts/S-1"}]}` +
		"\n" + scenariosFenceEnd
	oc2, err := ParseOutcome([]byte(sOut))
	if err != nil {
		t.Fatalf("ParseOutcome(scenarios) error = %v", err)
	}
	if len(oc2.Scenarios) != 1 || len(oc2.Questions) != 0 {
		t.Fatalf("want scenarios outcome, got %+v", oc2)
	}
}

func TestParseScenarios_DefaultsMissingHoldoutRef(t *testing.T) {
	// The director flow must not dead-end when the LLM omits hidden_holdout_ref after a
	// clarifying round: ParseScenarios derives the pg://holdouts/<id> convention default.
	out := scenariosFenceStart + "\n" +
		`{"scenarios":[{"id":"A-1","title":"ServiceCompany kaldir","lane":"general","tier":"T2",` +
		`"deps":[],"acceptance":["migration up/down 'yesil'"]}]}` + "\n" + scenariosFenceEnd
	sc, err := ParseScenarios([]byte(out))
	if err != nil {
		t.Fatalf("ParseScenarios(no ref) error = %v, want defaulted success", err)
	}
	if len(sc) != 1 || sc[0].HoldoutRef != "pg://holdouts/A-1" {
		t.Fatalf("holdout ref not defaulted to pg://holdouts/A-1: %+v", sc)
	}
}

func TestParseScenarios_PresentButInvalidRefStillRejected(t *testing.T) {
	// Defaulting only fills an EMPTY ref; a present-but-repo-relative ref is still rejected
	// (ADR-0018: the holdout must be repo-external).
	out := scenariosFenceStart + "\n" +
		`{"scenarios":[{"id":"A-1","title":"x","lane":"web","tier":"T2",` +
		`"deps":[],"acceptance":["ok"],"hidden_holdout_ref":"internal/x/holdout_test.go"}]}` + "\n" + scenariosFenceEnd
	if _, err := ParseScenarios([]byte(out)); err == nil || !errors.Is(err, ErrMalformedScenarios) {
		t.Fatalf("want ErrMalformedScenarios for repo-relative ref, got %v", err)
	}
}

func TestParseQuestions_TruncatesOverlongHeader(t *testing.T) {
	// The model sometimes emits a chip header over the 12-rune limit (esp. Turkish, e.g.
	// "Satır kapsamı" = 13 runes); truncate rather than dead-end the director on it.
	out := questionsFenceStart + "\n" +
		`{"questions":[{"question":"Hangi satırlar?","header":"Satır kapsamı","multi_select":false,` +
		`"options":[{"label":"Hepsi","description":"tüm satırlar"},{"label":"Seçili","description":"seçili olanlar"}]}]}` +
		"\n" + questionsFenceEnd
	qs, err := ParseQuestions([]byte(out))
	if err != nil {
		t.Fatalf("ParseQuestions(13-rune header) error = %v, want truncated success", err)
	}
	if n := utf8.RuneCountInString(qs[0].Header); n > maxHeaderChars {
		t.Fatalf("header not truncated: %q (%d runes > %d)", qs[0].Header, n, maxHeaderChars)
	}
	if qs[0].Header != "Satır kapsam" {
		t.Fatalf("header truncation = %q, want %q", qs[0].Header, "Satır kapsam")
	}
}

func TestParseHoldout_JSONBlock(t *testing.T) {
	// Holdout stays YAML-literal in the prompt, but JSON must still parse (yaml.v3
	// reads JSON) so a model that emits it as JSON is tolerated.
	out := holdoutFenceStart + "\n" +
		`{"files":{"e2e/remove.spec.ts":"import {test} from '@playwright/test';\ntest('x', async () => {});"}}` +
		"\n" + holdoutFenceEnd
	files, ok := ParseHoldout([]byte(out))
	if !ok {
		t.Fatalf("ParseHoldout(JSON) ok=false, want true")
	}
	if got := files["e2e/remove.spec.ts"]; !strings.Contains(got, "playwright") {
		t.Fatalf("holdout content lost: %q", got)
	}
}
