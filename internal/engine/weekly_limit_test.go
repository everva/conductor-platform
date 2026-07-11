package engine

import (
	"errors"
	"strings"
	"testing"
)

// The WEEKLY limit does not look like a failure: claude answers in a normal, EXIT-ZERO turn —
// "You've hit your weekly limit · resets 11am (UTC)" — and stops without a verdict. The rate-limit
// check used to be gated on a non-zero exit, so it never fired; the run failed as a malformed
// verdict and the runner churned the task into `blocked` instead of backing off. On 2026-07-11 that
// burned 15 backend tasks into false blocks in half an hour. A limit is a WALL, not a defect: it must
// surface as ErrRateLimited so the task goes back to ready and waits for the window to clear.
func TestClassifyOutput_WeeklyLimitOnExitZeroIsRateLimited(t *testing.T) {
	t.Parallel()

	// Real shape: the stream-json init line, then the assistant turn, and NO result object.
	stdout := []byte(`{"type":"system","subtype":"init","session_id":"x"}
{"type":"assistant","message":{"content":[{"type":"text","text":"You've hit your weekly limit · resets 11am (UTC)"}],"stop_reason":"stop_sequence"}}
`)

	err := classifyOutput(stdout, nil) // nil runErr: the process exited ZERO

	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("classifyOutput = %v, want ErrRateLimited (a limit is a wall, not a code failure)", err)
	}
}

// A run that merely NARRATES the phrase — and still produces a real verdict — must not be mistaken
// for a limit, or a healthy task would be bounced forever. The verdict is the discriminator.
func TestClassifyOutput_NarratedLimitWithAVerdictIsNotRateLimited(t *testing.T) {
	t.Parallel()

	stdout := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"I read the docs about the weekly limit and handled it."}]}}
{"type":"result","subtype":"success","result":"done"}
`)

	if err := classifyOutput(stdout, nil); err != nil {
		t.Fatalf("classifyOutput = %v, want nil: a run that produced a verdict is not limited", err)
	}
}

// A malformed verdict must say WHAT the performer produced. Reporting only the parser's
// disappointment ("no result-keyed JSON object") hid the weekly-limit answer behind an opaque line on
// all 15 blocked tasks — the reason was sitting in the output, unread.
func TestLastResultObject_MalformedVerdictSurfacesWhatThePerformerSaid(t *testing.T) {
	t.Parallel()

	stdout := []byte(`{"type":"system","subtype":"init"}
You've hit your weekly limit · resets 11am (UTC)
`)

	_, err := lastResultObject(stdout)

	if !errors.Is(err, ErrMalformedVerdict) {
		t.Fatalf("err = %v, want ErrMalformedVerdict", err)
	}
	if !strings.Contains(err.Error(), "weekly limit") {
		t.Fatalf("the malformed-verdict error hides the reason: %q", err.Error())
	}
}
