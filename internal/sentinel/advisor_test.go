package sentinel

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestParseAdvice_Valid covers the three valid verdicts, prose-wrapping, and the
// last-valid-block selection rule (the advisor may narrate, emit a draft, then the
// final verdict object last).
func TestParseAdvice_Valid(t *testing.T) {
	cases := []struct {
		name       string
		stdout     string
		wantAdvice Advice
		wantReason string
	}{
		{
			name:       "progressing bare",
			stdout:     `{"advice":"progressing","reason":"still compiling"}`,
			wantAdvice: AdviceProgressing,
			wantReason: "still compiling",
		},
		{
			name:       "stuck in prose",
			stdout:     "I looked at the log.\nThe run is wedged.\n{\"advice\":\"stuck\",\"reason\":\"retry loop\"}\n",
			wantAdvice: AdviceStuck,
			wantReason: "retry loop",
		},
		{
			name:       "needs_human with markdown fence",
			stdout:     "```json\n{\"advice\":\"needs_human\",\"reason\":\"login prompt\"}\n```",
			wantAdvice: AdviceNeedsHuman,
			wantReason: "login prompt",
		},
		{
			name:       "last valid block wins over earlier draft",
			stdout:     `{"advice":"progressing","reason":"draft"} ... final: {"advice":"stuck","reason":"final call"}`,
			wantAdvice: AdviceStuck,
			wantReason: "final call",
		},
		{
			name:       "missing reason is allowed",
			stdout:     `{"advice":"progressing"}`,
			wantAdvice: AdviceProgressing,
			wantReason: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adv, reason, err := ParseAdvice([]byte(tc.stdout))
			if err != nil {
				t.Fatalf("ParseAdvice(%q): unexpected error %v", tc.stdout, err)
			}
			if adv != tc.wantAdvice {
				t.Fatalf("advice = %q, want %q", adv, tc.wantAdvice)
			}
			if reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}

// TestParseAdvice_Malformed proves out-of-enum / undecodable advice is an ERROR,
// never coerced into a verdict (never-fake-advice).
func TestParseAdvice_Malformed(t *testing.T) {
	cases := []struct {
		name    string
		stdout  string
		wantErr error
	}{
		{"empty", "", ErrNoAdvice},
		{"prose only no json", "the run looks fine to me", ErrNoAdvice},
		{"json without advice key", `{"result":"pass"}`, ErrNoAdvice},
		{"out-of-enum advice", `{"advice":"maybe-ok","reason":"unsure"}`, ErrMalformedAdvice},
		{"empty advice value", `{"advice":"","reason":"x"}`, ErrMalformedAdvice},
		{"advice not a string", `{"advice":42}`, ErrMalformedAdvice},
		{"capitalized out-of-enum", `{"advice":"Progressing"}`, ErrMalformedAdvice},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adv, _, err := ParseAdvice([]byte(tc.stdout))
			if err == nil {
				t.Fatalf("ParseAdvice(%q) = %q, want error %v", tc.stdout, adv, tc.wantErr)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ParseAdvice(%q) err = %v, want errors.Is %v", tc.stdout, err, tc.wantErr)
			}
			if !errors.Is(err, ErrAdvisor) {
				t.Fatalf("all advisor parse errors must wrap ErrAdvisor, got %v", err)
			}
		})
	}
}

// fakeRunner returns fixture stdout/err without spawning any process, mirroring
// the CommandEngine test seam. It records the stdin it was handed so the prompt
// wiring can be asserted.
func fakeRunner(stdout string, err error) (runnerFunc, *[]byte) {
	var gotStdin []byte
	rf := func(_ context.Context, _ []string, _ string, stdin []byte, _ []string) ([]byte, error) {
		gotStdin = append([]byte(nil), stdin...)
		return []byte(stdout), err
	}
	return rf, &gotStdin
}

// TestCommandAdvisor_ParsesFixtureStdout proves the CommandAdvisor runs its
// (fake) runner and parses the verdict, with no real `claude -p`. It also confirms
// the prompt embedding the recent output flowed to the runner on stdin.
func TestCommandAdvisor_ParsesFixtureStdout(t *testing.T) {
	rf, gotStdin := fakeRunner(`narrating... {"advice":"stuck","reason":"hung on lock"}`, nil)
	ca := NewCommandAdvisor([]string{"claude", "-p"}, "/tmp", nil, rf)

	adv, reason, err := ca.Advise(context.Background(), "TAIL-OUTPUT-MARKER")
	if err != nil {
		t.Fatalf("Advise: %v", err)
	}
	if adv != AdviceStuck || reason != "hung on lock" {
		t.Fatalf("Advise = (%q,%q), want (stuck, hung on lock)", adv, reason)
	}
	if len(*gotStdin) == 0 {
		t.Fatalf("advisor must feed the prompt on stdin")
	}
	if !strings.Contains(string(*gotStdin), "TAIL-OUTPUT-MARKER") {
		t.Fatalf("advisor prompt must embed the recent output, got %q", string(*gotStdin))
	}
}

// TestCommandAdvisor_MalformedOutput_Error proves malformed advisor output
// surfaces as an error (Assess maps it to the conservative Continue) — never a
// fabricated verdict.
func TestCommandAdvisor_MalformedOutput_Error(t *testing.T) {
	rf, _ := fakeRunner("the run seems okay i guess", nil)
	ca := NewCommandAdvisor([]string{"claude", "-p"}, "/tmp", nil, rf)
	if _, _, err := ca.Advise(context.Background(), "x"); !errors.Is(err, ErrAdvisor) {
		t.Fatalf("malformed output: err = %v, want errors.Is ErrAdvisor", err)
	}
}

// TestCommandAdvisor_RunError_Empty_Error proves a process error with no usable
// output is a hard advisor failure.
func TestCommandAdvisor_RunError_Empty_Error(t *testing.T) {
	rf, _ := fakeRunner("", errors.New("exec failed"))
	ca := NewCommandAdvisor([]string{"claude", "-p"}, "/tmp", nil, rf)
	if _, _, err := ca.Advise(context.Background(), "x"); !errors.Is(err, ErrAdvisor) {
		t.Fatalf("run error empty: err = %v, want errors.Is ErrAdvisor", err)
	}
}

// TestCommandAdvisor_RunError_WithOutput_StillParses proves a process error that
// nonetheless produced a parseable verdict on stdout is still parsed (exit 1 but a
// valid advice object), mirroring the engine's tolerance.
func TestCommandAdvisor_RunError_WithOutput_StillParses(t *testing.T) {
	rf, _ := fakeRunner(`{"advice":"progressing","reason":"ok"}`, errors.New("exit status 1"))
	ca := NewCommandAdvisor([]string{"claude", "-p"}, "/tmp", nil, rf)
	adv, _, err := ca.Advise(context.Background(), "x")
	if err != nil {
		t.Fatalf("run error with usable output should parse: %v", err)
	}
	if adv != AdviceProgressing {
		t.Fatalf("advice = %q, want progressing", adv)
	}
}

// TestCommandAdvisor_NilRunner_Error proves a misconfigured advisor (nil runner)
// degrades to an error rather than panicking.
func TestCommandAdvisor_NilRunner_Error(t *testing.T) {
	ca := NewCommandAdvisor([]string{"claude"}, "/tmp", nil, nil)
	if _, _, err := ca.Advise(context.Background(), "x"); !errors.Is(err, ErrAdvisor) {
		t.Fatalf("nil runner: err = %v, want errors.Is ErrAdvisor", err)
	}
}
