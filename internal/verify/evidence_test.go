package verify

import (
	"strings"
	"testing"
)

// A recipe gate is a multi-step shell script under `set -e`: it prints a banner FIRST
// and dies AT the failing step, so the error is the LAST thing it writes. Reporting the
// head made every xirigo-admin gate failure read "verify: node v22.23.0 / npm 10.9.8" —
// a version string, not an error. That string was fed back as the self-correction
// feedback and injected into the next attempt's acceptance ("Resolve this prior-review
// finding: …"), so the developer had nothing actionable, changed nothing, and the task
// blocked as "developer made no change". Evidence must carry the failing step, never the
// banner alone.
func TestFailureEvidence_ReportsTheFailingStepNotTheBanner(t *testing.T) {
	t.Parallel()

	out := []byte(strings.Join([]string{
		"verify: node v22.23.0 / npm 10.9.8",
		"verify: [1/5 HARD] npm ci",
		"added 812 packages in 21s",
		"verify: [2/5 HARD] typecheck",
		"src/components/orders/order-print.ts(129,14): error TS2551: Property 'tax' does not exist.",
		"npm ERR! code ELIFECYCLE",
	}, "\n"))

	ev := failureEvidence(out)

	if strings.Contains(ev, "node v22.23.0") {
		t.Fatalf("evidence still leads with the version banner: %q", ev)
	}
	if !strings.Contains(ev, "error TS2551") {
		t.Fatalf("evidence dropped the real error; got %q", ev)
	}
	if !strings.Contains(ev, "[2/5 HARD] typecheck") {
		t.Fatalf("evidence dropped the failing step; got %q", ev)
	}
}

// A gate that fails on its very first line (a one-shot compiler, no banner) must still
// surface that line — the tail of a 1-line output IS that line.
func TestFailureEvidence_SingleLineFailure(t *testing.T) {
	t.Parallel()

	ev := failureEvidence([]byte("main.go:7:2: undefined: doesNotExist\n"))

	if ev != "main.go:7:2: undefined: doesNotExist" {
		t.Fatalf("evidence = %q, want the compiler error verbatim", ev)
	}
}

// A gate that fails silently must say so — never fake-green, never an empty reason.
func TestFailureEvidence_NoOutput(t *testing.T) {
	t.Parallel()

	if ev := failureEvidence(nil); ev != "exit non-zero (no output)" {
		t.Fatalf("evidence = %q, want the explicit no-output signal", ev)
	}
	if ev := failureEvidence([]byte("\n  \n\n")); ev != "exit non-zero (no output)" {
		t.Fatalf("whitespace-only evidence = %q, want the explicit no-output signal", ev)
	}
}

// Evidence is bounded so a gate that dumps a megabyte of logs cannot blow up the task
// reason — but the bound must keep the END (nearest the failure), not the start.
func TestFailureEvidence_BoundedKeepingTheEnd(t *testing.T) {
	t.Parallel()

	var b strings.Builder
	for i := 0; i < 400; i++ {
		b.WriteString("noisy build chatter line that says nothing useful at all\n")
	}
	b.WriteString("FATAL: the actual failure\n")

	ev := failureEvidence([]byte(b.String()))

	if len(ev) > evidenceMax+len("…") {
		t.Fatalf("evidence len = %d, want <= %d", len(ev), evidenceMax+len("…"))
	}
	if !strings.HasSuffix(ev, "FATAL: the actual failure") {
		t.Fatalf("bounding dropped the failure; evidence = %q", ev)
	}
}
