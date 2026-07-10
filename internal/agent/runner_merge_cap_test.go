package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// errSquashConflict mimics the git merger's squash-conflict error — the one that wedged
// xirigo-vendor's V-44: it repeats identically on every retry, so no amount of
// re-leasing can clear it.
var errSquashConflict = errors.New(`squash "conductor/p/T-1": CONFLICT (modify/delete): .conductor/REVIEW.md deleted in HEAD`)

// TestRunOnce_MergeFailsRepeatedly_BlocksInsteadOfLooping proves the retry loop is bounded.
// The FIRST merge failure still returns an error and reports NO verdict — the lease-release
// reverts the task running→ready so a re-cut-from-base + re-develop can self-heal a merge
// that only failed because the base advanced. The SECOND consecutive failure on the same
// task reports it BLOCKED with the git error as the board's reason, so the agent stops
// burning a shared host's CPU on a conflict retrying cannot clear.
func TestRunOnce_MergeFailsRepeatedly_BlocksInsteadOfLooping(t *testing.T) {
	ctx := context.Background()
	gw := &fakeGateway{leaseTask: leased(), resultDecision: "merge"}
	ex := &fakeExecutor{out: RunOutcome{Result: "pass", Branch: "conductor/p/T-1"}, mergeErr: errSquashConflict}
	r := newRunner(gw, ex)

	out, err := r.RunOnce(ctx)
	if err == nil {
		t.Fatalf("first merge failure must surface as an error (no verdict → task reverts to ready); got out=%q", out)
	}
	if gw.resultReport.Result != "pass" {
		t.Fatalf("first failure must not report a blocked verdict, got %q", gw.resultReport.Result)
	}

	out, err = r.RunOnce(ctx)
	if err != nil {
		t.Fatalf("second consecutive merge failure must be reported, not errored: %v", err)
	}
	if out != OutcomeBlocked {
		t.Fatalf("outcome = %q, want %q", out, OutcomeBlocked)
	}
	if gw.resultReport.Result != "blocked" {
		t.Fatalf("reported result = %q, want blocked", gw.resultReport.Result)
	}
	if !strings.Contains(gw.resultReport.Summary, "merge failed 2 consecutive times") {
		t.Fatalf("summary must say why it stopped retrying, got: %q", gw.resultReport.Summary)
	}
	if !strings.Contains(gw.resultReport.Summary, "CONFLICT (modify/delete)") {
		t.Fatalf("summary must carry the git conflict for the director, got: %q", gw.resultReport.Summary)
	}
	if ex.merges != MaxMergeAttempts {
		t.Fatalf("executor merges = %d, want exactly %d (no further retries)", ex.merges, MaxMergeAttempts)
	}
}

// TestRunOnce_MergeSucceeds_ResetsFailureStreak proves the cap counts CONSECUTIVE failures:
// a transient failure followed by a successful merge must not leave the task one strike from
// being blocked the next time the base races it.
func TestRunOnce_MergeSucceeds_ResetsFailureStreak(t *testing.T) {
	ctx := context.Background()
	gw := &fakeGateway{leaseTask: leased(), resultDecision: "merge"}
	ex := &fakeExecutor{out: RunOutcome{Result: "pass", Branch: "conductor/p/T-1"}, mergeErr: errSquashConflict}
	r := newRunner(gw, ex)

	if _, err := r.RunOnce(ctx); err == nil {
		t.Fatalf("want first merge to fail")
	}

	ex.mergeErr, ex.mergeSHA = nil, "9f2c1ab"
	if out, err := r.RunOnce(ctx); err != nil || out != OutcomeMerged {
		t.Fatalf("merge should succeed: out=%q err=%v", out, err)
	}

	// Streak was cleared by the success: the next failure is a FIRST failure again.
	ex.mergeErr, ex.mergeSHA = errSquashConflict, ""
	out, err := r.RunOnce(ctx)
	if err == nil {
		t.Fatalf("post-success failure must be treated as the first of a new streak, got out=%q", out)
	}
	if gw.resultReport.Result == "blocked" {
		t.Fatalf("a single failure after a success must not block the task")
	}
}
