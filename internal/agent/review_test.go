package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/everva/conductor-platform/internal/agentclient"
	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/statestore"
	"github.com/everva/conductor-platform/internal/verify"
)

// (1) FAIL-CLOSED mapping. runReviewClaude must NEVER auto-pass on an infra failure: with no
// `claude` on PATH the LookPath fails, and the reviewer maps to changes-requested with a NIL error
// (so the caller treats it as a normal held verdict, not a crash). This is the safety property —
// an unreachable/broken third-eye reviewer holds for the director, it never fakes green.
func TestRunReviewClaude_FailClosed_NoClaude(t *testing.T) {
	t.Setenv("PATH", "") // exec.LookPath("claude") now fails deterministically

	rr, err := runReviewClaude(context.Background(), t.TempDir(), []string{"build green"}, "@@ -0,0 +1 @@\n+x\n", nil)
	if err != nil {
		t.Fatalf("fail-closed must return a NIL error (a normal changes-requested), got %v", err)
	}
	if rr.Result != "changes-requested" {
		t.Fatalf("reviewer-unavailable must be changes-requested (never pass), got %q", rr.Result)
	}
	if rr.Result == "pass" {
		t.Fatalf("an infra failure must NEVER auto-pass")
	}
	if strings.TrimSpace(rr.Summary) == "" {
		t.Fatalf("fail-closed must carry an honest summary, got empty")
	}
}

// (2) writeReviewFeedback writes .conductor/REVIEW.md containing the findings + summary, so the
// re-develop performer reads exactly what it must fix.
func TestWriteReviewFeedback_WritesFindings(t *testing.T) {
	ws := t.TempDir()
	r := engine.ReviewResult{
		Result:   "changes-requested",
		Findings: []string{"api/handler.go:42 — nil deref on req.User; guard it", "web/x.ts:10 — missing await"},
		Summary:  "checked bugs/security/correctness; two real defects",
	}
	if err := writeReviewFeedback(ws, r); err != nil {
		t.Fatalf("writeReviewFeedback: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(ws, ".conductor", "REVIEW.md"))
	if err != nil {
		t.Fatalf("REVIEW.md not written: %v", err)
	}
	s := string(body)
	if !strings.Contains(s, "# STRICT reviewer findings") {
		t.Fatalf("REVIEW.md missing the must-fix header:\n%s", s)
	}
	for _, f := range r.Findings {
		if !strings.Contains(s, f) {
			t.Fatalf("REVIEW.md missing finding %q:\n%s", f, s)
		}
	}
	if !strings.Contains(s, r.Summary) {
		t.Fatalf("REVIEW.md missing the reviewer summary:\n%s", s)
	}
}

// --- reviewLoop fakes (mirror runner_test.go's fake style) ---

// fakeDeveloper scripts e.eng.Develop for reviewLoop tests. It just counts re-develops; the gate
// verdict + the diff are scripted separately.
type fakeDeveloper struct {
	err  error
	runs int
}

func (f *fakeDeveloper) Develop(_ context.Context, _ statestore.Task, _ engine.Workspace) (engine.Verdict, error) {
	f.runs++
	return engine.Verdict{}, f.err
}

// fakeGate scripts e.verf.Verify (the DETERMINISTIC gate) for reviewLoop tests. result is the gate
// verdict returned on every re-verify (the gate STILL rules after a re-develop).
type fakeGate struct {
	result   string   // "pass" | "changes-requested"
	findings []string // carried on the returned ReviewResult (e.g. the gate's build/lint errors)
	err      error
	runs     int
}

func (f *fakeGate) Verify(_ context.Context, _ engine.Verdict, _ engine.Workspace, _ []verify.Gate, _ string) (engine.ReviewResult, []engine.Check, error) {
	f.runs++
	return engine.ReviewResult{Result: f.result, Findings: f.findings}, nil, f.err
}

// loopExecutor builds a RealExecutor with ONLY the reviewLoop collaborators wired (eng/verf/review/
// patch) — enough to drive reviewLoop in isolation without a provisioner, merger, or real git.
func loopExecutor(dev developer, gate gateVerifier, review reviewFunc, patch patchFunc) *RealExecutor {
	return &RealExecutor{
		eng:    dev,
		verf:   gate,
		review: review,
		patch:  patch,
		ws:     map[string]engine.Workspace{},
		phase:  map[string]string{},
	}
}

// scriptedReviewer returns a reviewFunc that replays the given results in order (the last is
// repeated once exhausted), counting calls.
func scriptedReviewer(results ...engine.ReviewResult) (*int, reviewFunc) {
	calls := 0
	fn := func(_ context.Context, _ string, _ []string, _ string, _ func(string)) (engine.ReviewResult, error) {
		i := calls
		if i >= len(results) {
			i = len(results) - 1
		}
		calls++
		return results[i], nil
	}
	return &calls, fn
}

// changingPatch returns a patchFunc that yields a DIFFERENT diff on each call, so the no-op guard
// never fires (each re-develop "changed" the tree). Mirrors a real re-develop that edits files.
func changingPatch() patchFunc {
	n := 0
	return func(_ context.Context, _ statestore.Project, _ engine.Workspace) string {
		n++
		return "diff-rev-" + string(rune('0'+n))
	}
}

func reviewLoopArgs() (statestore.Project, statestore.Task, engine.Workspace, agentclient.ScenarioInfo, engine.ReviewResult) {
	return statestore.Project{ID: "p", BaseBranch: "develop"},
		statestore.Task{ID: "T-1", ProjectID: "p"},
		engine.Workspace{Path: "/tmp/ws", Branch: "conductor/p/T-1"},
		agentclient.ScenarioInfo{Acceptance: []string{"build green"}},
		engine.ReviewResult{Result: "pass", Summary: "deterministic gate passed"}
}

// (3a) A first-read PASS keeps the deterministic gate's pass with NO re-develop — the happy path.
func TestReviewLoop_CleanFirstRead_Passes(t *testing.T) {
	dev := &fakeDeveloper{}
	gate := &fakeGate{result: "pass"}
	calls, reviewer := scriptedReviewer(engine.ReviewResult{Result: "pass", Summary: "clean"})
	e := loopExecutor(dev, gate, reviewer, changingPatch())

	project, task, ws, sc, gatePass := reviewLoopArgs()
	got := e.reviewLoop(context.Background(), project, task, ws, sc, gatePass)

	if got.Result != "pass" {
		t.Fatalf("clean first review must keep pass, got %q (%s)", got.Result, got.Summary)
	}
	if dev.runs != 0 {
		t.Fatalf("a clean first review must NOT re-develop, runs=%d", dev.runs)
	}
	if *calls != 1 {
		t.Fatalf("clean first review should review exactly once, got %d", *calls)
	}
}

// (3b) changes-requested THEN pass: the loop writes feedback, re-develops, re-verifies (gate still
// passes), re-reviews, and the corrected review passes → merge proceeds.
func TestReviewLoop_ChangesThenPass_Corrects(t *testing.T) {
	dev := &fakeDeveloper{}
	gate := &fakeGate{result: "pass"}
	calls, reviewer := scriptedReviewer(
		engine.ReviewResult{Result: "changes-requested", Findings: []string{"x.go:1 — fix"}},
		engine.ReviewResult{Result: "pass", Summary: "now clean"},
	)
	e := loopExecutor(dev, gate, reviewer, changingPatch())

	project, task, ws, sc, gatePass := reviewLoopArgs()
	got := e.reviewLoop(context.Background(), project, task, ws, sc, gatePass)

	if got.Result != "pass" {
		t.Fatalf("a corrected re-review must pass, got %q (%s)", got.Result, got.Summary)
	}
	if dev.runs != 1 {
		t.Fatalf("exactly one re-develop expected, got %d", dev.runs)
	}
	if gate.runs != 1 {
		t.Fatalf("exactly one re-verify expected, got %d", gate.runs)
	}
	if *calls != 2 {
		t.Fatalf("expected two reviews (changes-requested then pass), got %d", *calls)
	}
}

// (3c) PERSISTENT changes-requested exhausts the cap and ends as changes-requested ("needs user")
// — never a fabricated pass. The diff changes each round (so the no-op guard does not short-circuit
// it), isolating the exhaustion path.
func TestReviewLoop_PersistentChanges_ExhaustsToUnresolved(t *testing.T) {
	dev := &fakeDeveloper{}
	gate := &fakeGate{result: "pass"}
	calls, reviewer := scriptedReviewer(engine.ReviewResult{Result: "changes-requested", Findings: []string{"x.go:1 — still wrong"}})
	e := loopExecutor(dev, gate, reviewer, changingPatch())

	project, task, ws, sc, gatePass := reviewLoopArgs()
	got := e.reviewLoop(context.Background(), project, task, ws, sc, gatePass)

	if got.Result != "changes-requested" {
		t.Fatalf("persistent changes-requested must NOT pass, got %q", got.Result)
	}
	if !strings.Contains(got.Summary, "unresolved") || !strings.Contains(got.Summary, "needs user") {
		t.Fatalf("exhaustion summary should say unresolved/needs user, got %q", got.Summary)
	}
	if dev.runs != MaxReviewRounds {
		t.Fatalf("exhaustion must re-develop MaxReviewRounds(=%d) times, got %d", MaxReviewRounds, dev.runs)
	}
	if len(got.Findings) == 0 {
		t.Fatalf("exhaustion should carry the reviewer's last findings for the director")
	}
	_ = calls
}

// (3d) The DETERMINISTIC gate STILL rules: if a re-develop BREAKS the gate, reviewLoop returns the
// gate's changes-requested (Rule#9 — the gate is the merge authority), regardless of the reviewer.
func TestReviewLoop_ReDevelopBreaksGate_GateRules(t *testing.T) {
	dev := &fakeDeveloper{}
	gate := &fakeGate{result: "changes-requested"} // the re-verify now FAILS the gate
	calls, reviewer := scriptedReviewer(engine.ReviewResult{Result: "changes-requested", Findings: []string{"x.go:1 — fix"}})
	e := loopExecutor(dev, gate, reviewer, changingPatch())

	project, task, ws, sc, gatePass := reviewLoopArgs()
	got := e.reviewLoop(context.Background(), project, task, ws, sc, gatePass)

	if got.Result != "changes-requested" {
		t.Fatalf("a re-develop that breaks the gate must yield the gate's changes-requested, got %q", got.Result)
	}
	if dev.runs != 1 || gate.runs != 1 {
		t.Fatalf("expected one re-develop + one re-verify then stop, got dev=%d gate=%d", dev.runs, gate.runs)
	}
	// The reviewer is consulted exactly once (the initial read); after the broken gate it is NOT
	// consulted again — the gate decided.
	if *calls != 1 {
		t.Fatalf("the gate must decide before a second review, reviews=%d", *calls)
	}
}

// (3e) NO-OP guard: a re-develop that produces the SAME diff is unresolved (the developer made no
// change), so reviewLoop holds for the director rather than re-reviewing an identical diff forever.
func TestReviewLoop_NoChangeReDevelop_Unresolved(t *testing.T) {
	dev := &fakeDeveloper{}
	gate := &fakeGate{result: "pass"}
	calls, reviewer := scriptedReviewer(engine.ReviewResult{Result: "changes-requested", Findings: []string{"x.go:1 — fix"}})
	// A constant patch → the re-develop "changed nothing".
	constPatch := func(_ context.Context, _ statestore.Project, _ engine.Workspace) string { return "same-diff" }
	e := loopExecutor(dev, gate, reviewer, constPatch)

	project, task, ws, sc, gatePass := reviewLoopArgs()
	got := e.reviewLoop(context.Background(), project, task, ws, sc, gatePass)

	if got.Result != "changes-requested" {
		t.Fatalf("a no-op re-develop must stay changes-requested, got %q", got.Result)
	}
	if !strings.Contains(got.Summary, "made no change") {
		t.Fatalf("no-op summary should say the developer made no change, got %q", got.Summary)
	}
	if *calls != 1 {
		t.Fatalf("after a no-op re-develop the reviewer is NOT consulted again, reviews=%d", *calls)
	}
}

// (3f) A re-develop that hard-FAILS (no commit) is changes-requested — never a fabricated pass.
func TestReviewLoop_ReDevelopFails_ChangesRequested(t *testing.T) {
	dev := &fakeDeveloper{err: errors.New("develop: empty output")}
	gate := &fakeGate{result: "pass"}
	calls, reviewer := scriptedReviewer(engine.ReviewResult{Result: "changes-requested", Findings: []string{"x.go:1 — fix"}})
	e := loopExecutor(dev, gate, reviewer, changingPatch())

	project, task, ws, sc, gatePass := reviewLoopArgs()
	got := e.reviewLoop(context.Background(), project, task, ws, sc, gatePass)

	if got.Result != "changes-requested" {
		t.Fatalf("a failed re-develop must be changes-requested, got %q", got.Result)
	}
	if !strings.Contains(got.Summary, "re-develop after review failed") {
		t.Fatalf("summary should name the re-develop failure, got %q", got.Summary)
	}
	_ = gate
	_ = calls
}

// --- gateCorrectLoop (DETERMINISTIC-gate self-correction) ---
// These mirror the reviewLoop tests above but drive the gate-fail correction path: after the gate
// REJECTS the change, the AGENT re-develops to FIX it itself (cap MaxGateRounds) instead of blocking.
// The reviewer is NOT involved here (the gate is the authority); only eng/verf/patch are exercised.

// (4) writeGateFeedback writes .conductor/GATE.md with the gate's findings + summary AND the strong
// "re-run the build/lint yourself and fix every error" directive, so the re-develop performer reads
// exactly what the deterministic gate flagged.
func TestWriteGateFeedback_WritesFindingsAndDirective(t *testing.T) {
	ws := t.TempDir()
	r := engine.ReviewResult{
		Result:   "changes-requested",
		Findings: []string{"packages/shared/x.ts:10 — missing explicit return type", "build: pnpm build exited 1"},
		Summary:  "build failed; lint had 3 problems",
	}
	if err := writeGateFeedback(ws, r); err != nil {
		t.Fatalf("writeGateFeedback: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(ws, ".conductor", "GATE.md"))
	if err != nil {
		t.Fatalf("GATE.md not written: %v", err)
	}
	s := string(body)
	if !strings.Contains(s, "# The deterministic GATE FAILED — fix it before anything else") {
		t.Fatalf("GATE.md missing the must-fix header:\n%s", s)
	}
	// The strong directive must be present verbatim in spirit (build + lint-strict shared).
	if !strings.Contains(s, "pnpm build") || !strings.Contains(s, "packages/shared is lint-strict") {
		t.Fatalf("GATE.md missing the re-run-checks directive:\n%s", s)
	}
	for _, f := range r.Findings {
		if !strings.Contains(s, f) {
			t.Fatalf("GATE.md missing finding %q:\n%s", f, s)
		}
	}
	if !strings.Contains(s, r.Summary) {
		t.Fatalf("GATE.md missing the gate summary:\n%s", s)
	}
}

// gateFailArgs mirrors reviewLoopArgs but the seed "review" is a gate changes-requested (the loop is
// entered only AFTER the first verify already failed), carrying the gate's findings.
func gateFailArgs() (statestore.Project, statestore.Task, engine.Workspace, agentclient.ScenarioInfo, engine.ReviewResult) {
	return statestore.Project{ID: "p", BaseBranch: "develop"},
		statestore.Task{ID: "T-1", ProjectID: "p"},
		engine.Workspace{Path: "/tmp/ws", Branch: "conductor/p/T-1"},
		agentclient.ScenarioInfo{Acceptance: []string{"build green"}},
		engine.ReviewResult{Result: "changes-requested", Findings: []string{"build: pnpm build exited 1"}, Summary: "deterministic gate failed"}
}

// (5a) gate-fail THEN pass: the loop writes GATE.md, re-develops, re-verifies — the gate now passes,
// so the loop returns the gate pass (Run then flows into the third-eye review).
func TestGateCorrectLoop_FailThenPass_Corrects(t *testing.T) {
	dev := &fakeDeveloper{}
	gate := &fakeGate{result: "pass"} // the re-verify after the re-develop now PASSES
	calls, reviewer := scriptedReviewer(engine.ReviewResult{Result: "pass"})
	e := loopExecutor(dev, gate, reviewer, changingPatch())

	project, task, ws, sc, gateFail := gateFailArgs()
	got := e.gateCorrectLoop(context.Background(), project, task, ws, sc, gateFail)

	if got.Result != "pass" {
		t.Fatalf("a corrected gate must pass, got %q (%s)", got.Result, got.Summary)
	}
	if dev.runs != 1 {
		t.Fatalf("exactly one re-develop expected, got %d", dev.runs)
	}
	if gate.runs != 1 {
		t.Fatalf("exactly one re-verify expected, got %d", gate.runs)
	}
	// The reviewer is NOT the gate loop's authority — gateCorrectLoop never calls it.
	if *calls != 0 {
		t.Fatalf("gateCorrectLoop must NOT consult the third-eye reviewer, reviews=%d", *calls)
	}
}

// (5b) PERSISTENT gate failure exhausts the cap and ends as changes-requested ("needs user") — never
// a fabricated pass. The diff changes each round (so the no-progress guard does not short-circuit it),
// isolating the exhaustion path.
func TestGateCorrectLoop_PersistentFail_ExhaustsToUnresolved(t *testing.T) {
	dev := &fakeDeveloper{}
	gate := &fakeGate{result: "changes-requested", findings: []string{"build: still failing"}} // the re-verify keeps FAILING the gate
	calls, reviewer := scriptedReviewer(engine.ReviewResult{Result: "pass"})
	e := loopExecutor(dev, gate, reviewer, changingPatch())

	project, task, ws, sc, gateFail := gateFailArgs()
	got := e.gateCorrectLoop(context.Background(), project, task, ws, sc, gateFail)

	if got.Result != "changes-requested" {
		t.Fatalf("persistent gate failure must NOT pass, got %q", got.Result)
	}
	if !strings.Contains(got.Summary, "unresolved") || !strings.Contains(got.Summary, "needs user") {
		t.Fatalf("exhaustion summary should say unresolved/needs user, got %q", got.Summary)
	}
	if dev.runs != MaxGateRounds {
		t.Fatalf("exhaustion must re-develop MaxGateRounds(=%d) times, got %d", MaxGateRounds, dev.runs)
	}
	if len(got.Findings) == 0 {
		t.Fatalf("exhaustion should carry the gate's last findings for the director")
	}
	if *calls != 0 {
		t.Fatalf("gateCorrectLoop must NOT consult the reviewer, reviews=%d", *calls)
	}
}

// (5c) NO-PROGRESS guard: a re-develop that produces the SAME diff means the developer changed
// nothing, so gateCorrectLoop holds for the director rather than re-verifying an identical diff
// forever. The constant patch makes the seed == the post-re-develop diff → the guard fires on round 1.
func TestGateCorrectLoop_NoProgress_Breaks(t *testing.T) {
	dev := &fakeDeveloper{}
	gate := &fakeGate{result: "changes-requested", findings: []string{"build: still failing"}} // still failing, so the guard (not a pass) decides
	calls, reviewer := scriptedReviewer(engine.ReviewResult{Result: "pass"})
	// A constant patch → the re-develop "changed nothing" (seed == post-re-develop diff).
	constPatch := func(_ context.Context, _ statestore.Project, _ engine.Workspace) string { return "same-diff" }
	e := loopExecutor(dev, gate, reviewer, constPatch)

	project, task, ws, sc, gateFail := gateFailArgs()
	got := e.gateCorrectLoop(context.Background(), project, task, ws, sc, gateFail)

	if got.Result != "changes-requested" {
		t.Fatalf("a no-progress re-develop must stay changes-requested, got %q", got.Result)
	}
	if !strings.Contains(got.Summary, "made no change") {
		t.Fatalf("no-progress summary should say the developer made no change, got %q", got.Summary)
	}
	if dev.runs != 1 {
		t.Fatalf("the guard must fire after exactly one re-develop, got %d", dev.runs)
	}
	if gate.runs != 1 {
		t.Fatalf("exactly one re-verify before the guard fires, got %d", gate.runs)
	}
	if len(got.Findings) == 0 {
		t.Fatalf("the no-progress hold should carry the gate's findings")
	}
	_ = calls
}

// (5d) A re-develop that hard-FAILS (no commit) is changes-requested — never a fabricated pass. With
// ws.Path not a git repo, aheadOfBase is false, so a dev error is treated as a real no-commit failure.
func TestGateCorrectLoop_ReDevelopFails_ChangesRequested(t *testing.T) {
	dev := &fakeDeveloper{err: errors.New("develop: empty output")}
	gate := &fakeGate{result: "pass"}
	calls, reviewer := scriptedReviewer(engine.ReviewResult{Result: "pass"})
	e := loopExecutor(dev, gate, reviewer, changingPatch())

	project, task, ws, sc, gateFail := gateFailArgs()
	got := e.gateCorrectLoop(context.Background(), project, task, ws, sc, gateFail)

	if got.Result != "changes-requested" {
		t.Fatalf("a failed re-develop must be changes-requested, got %q", got.Result)
	}
	if !strings.Contains(got.Summary, "re-develop after gate failure failed") {
		t.Fatalf("summary should name the re-develop failure, got %q", got.Summary)
	}
	if gate.runs != 0 {
		t.Fatalf("a no-commit re-develop must NOT reach the re-verify, gate.runs=%d", gate.runs)
	}
	_ = calls
}
