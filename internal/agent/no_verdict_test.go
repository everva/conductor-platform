package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/everva/conductor-platform/internal/agentclient"
)


// Minimal fakes local to this file: the shared ones in runner_test.go keep only the LAST result and
// a single leased task, and these tests are precisely about a SEQUENCE of runs.
type nvGateway struct {
	queue    []agentclient.LeasedTask
	results  []agentclient.ResultReport
	released []string
}

func newFakeGateway() *nvGateway { return &nvGateway{} }

func (g *nvGateway) Lease(_ context.Context, _, _ string, _ []string) (agentclient.LeasedTask, bool, error) {
	if len(g.queue) == 0 {
		return agentclient.LeasedTask{}, false, nil
	}
	t := g.queue[0]
	g.queue = g.queue[1:]
	return t, true, nil
}
func (g *nvGateway) Scenarios(_ context.Context, _ string) ([]agentclient.ScenarioInfo, error) {
	return nil, nil
}
func (g *nvGateway) Report(_ context.Context, _, _, _, _ string, _ map[string]any) error { return nil }
func (g *nvGateway) Result(_ context.Context, _, _ string, r agentclient.ResultReport) (string, error) {
	g.results = append(g.results, r)
	return "blocked", nil
}
func (g *nvGateway) StoreTaskDiff(_ context.Context, _, _ string, _ agentclient.TaskDiffBody) error {
	return nil
}
func (g *nvGateway) Decision(_ context.Context, _, _ string) (string, error) { return "blocked", nil }
func (g *nvGateway) Merged(_ context.Context, _, _, _ string) error          { return nil }
func (g *nvGateway) Release(_ context.Context, _, _, taskID string) error {
	g.released = append(g.released, taskID)
	return nil
}
func (g *nvGateway) Heartbeat(_ context.Context, _ string) error { return nil }

type fakeExec struct {
	out    RunOutcome
	runErr error
}

func (e *fakeExec) Run(_ context.Context, _ agentclient.TaskInfo, _ agentclient.ScenarioInfo) (RunOutcome, error) {
	return e.out, e.runErr
}
func (e *fakeExec) Merge(_ context.Context, _ agentclient.TaskInfo, _ agentclient.ScenarioInfo, _ string, _ bool) (MergeResult, error) {
	return MergeResult{SHA: "sha"}, nil
}
func (e *fakeExec) Cleanup(_ context.Context, _ agentclient.TaskInfo) {}

// A run that produces no verdict produces no evidence. These tests pin the consequence: such a run
// never becomes a review item, and the agent tells "this task is unworkable" apart from "the machine
// is down" before it dares to block anything.

// errWalled is what a usage-limited performer actually looks like from the engine: the process exits
// fine, prints a sentence, and there is no result-keyed JSON anywhere in it.
var errWalled = errors.New(`engine: malformed verdict: no result-keyed JSON object in output; performer said: You've hit your weekly limit · resets 11am (UTC)`)

// THE 2026-07-11 INCIDENT, REPLAYED. A weekly limit walled the account and the backend agent leased
// task after task into it. Every run came back with no verdict — and 21 of them were written into
// the board as `blocked`, i.e. as things the director must review. Not one was a defect.
// With the fix: nothing is blocked, every task goes back to the queue, and the agent backs off.
func TestNoVerdict_AWalledAccountBlocksNothing(t *testing.T) {
	gw := newFakeGateway()
	// Five different tasks, in the order the agent would lease them.
	for _, id := range []string{"B-1", "B-2", "B-3", "B-4", "B-5"} {
		gw.queue = append(gw.queue, agentclient.LeasedTask{Task: agentclient.TaskInfo{ID: id, ProjectID: "p"}})
	}
	ex := &fakeExec{runErr: errWalled} // the wall: never a verdict, for any task
	r := New(gw, ex, Config{ProjectID: "p", HostID: "h"})

	for i := 0; i < 5; i++ {
		out, err := r.RunOnce(context.Background())
		if err != nil {
			t.Fatalf("run %d: unexpected error: %v", i, err)
		}
		if out != OutcomeNoVerdict {
			t.Fatalf("run %d: want OutcomeNoVerdict, got %q", i, out)
		}
	}

	if len(gw.results) != 0 {
		t.Fatalf("a walled account reported %d verdict(s) — it must report NONE (got %+v)", len(gw.results), gw.results)
	}
	if len(gw.released) != 5 {
		t.Errorf("every walled task must be released back to ready: want 5 releases, got %d", len(gw.released))
	}
}

// The environment breaker must not be fooled into silence forever: when the SAME task keeps coming
// back empty while the environment is demonstrably fine (other tasks produce verdicts in between),
// that task IS the problem, and the director should see it — with the performer's real words.
func TestNoVerdict_OneUnworkableTaskIsEventuallyBlocked_WithRealEvidence(t *testing.T) {
	gw := newFakeGateway()
	ex := &fakeExec{}
	r := New(gw, ex, Config{ProjectID: "p", HostID: "h"})
	bad := agentclient.LeasedTask{Task: agentclient.TaskInfo{ID: "B-bad", ProjectID: "p"}}

	for attempt := 1; attempt <= MaxNoVerdictAttempts; attempt++ {
		gw.queue = append(gw.queue, bad)
		ex.runErr = errWalled
		out, err := r.RunOnce(context.Background())
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if attempt < MaxNoVerdictAttempts {
			if out != OutcomeNoVerdict {
				t.Fatalf("attempt %d: want OutcomeNoVerdict (retry), got %q", attempt, out)
			}
			if len(gw.results) != 0 {
				t.Fatalf("attempt %d: blocked too early — nothing may be reported before %d attempts", attempt, MaxNoVerdictAttempts)
			}
		}
	}

	if len(gw.results) != 1 {
		t.Fatalf("after %d empty runs of the SAME task, want exactly 1 reported verdict, got %d", MaxNoVerdictAttempts, len(gw.results))
	}
	got := gw.results[0]
	if got.Result != "blocked" {
		t.Errorf("want blocked, got %q", got.Result)
	}
	// The summary must say what the PERFORMER said — not what the parser wished for. The old
	// summary ("agent run failed: … malformed verdict …") described the harness and taught the
	// director nothing.
	if !strings.Contains(got.Summary, "weekly limit") {
		t.Errorf("the block must carry the performer's actual last words as evidence, got: %q", got.Summary)
	}
	if !strings.Contains(got.Summary, "other tasks succeeded") {
		t.Errorf("the block must say WHY it is honest (the environment was working), got: %q", got.Summary)
	}
}

// A verdict — any verdict — is proof the environment works. It must clear the streak, so that an
// earlier wall can never leak into a later block.
func TestNoVerdict_ASuccessfulRunClearsTheStreak(t *testing.T) {
	gw := newFakeGateway()
	ex := &fakeExec{}
	r := New(gw, ex, Config{ProjectID: "p", HostID: "h"})
	bad := agentclient.LeasedTask{Task: agentclient.TaskInfo{ID: "B-bad", ProjectID: "p"}}

	// Two empty runs of the same task…
	for i := 0; i < 2; i++ {
		gw.queue = append(gw.queue, bad)
		ex.runErr = errWalled
		if _, err := r.RunOnce(context.Background()); err != nil {
			t.Fatalf("empty run %d: %v", i, err)
		}
	}
	// …then the SAME task runs clean (the wall lifted).
	gw.queue = append(gw.queue, bad)
	ex.runErr = nil
	ex.out = RunOutcome{Result: "pass", Branch: "conductor/B-bad", Summary: "ok"}
	if _, err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("clean run: %v", err)
	}
	// …and later goes empty ONCE more. That must NOT be attempt 3 of the old streak.
	gw.queue = append(gw.queue, bad)
	ex.runErr = errWalled
	out, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("post-success empty run: %v", err)
	}
	if out != OutcomeNoVerdict {
		t.Errorf("the streak was not cleared by the successful run: want OutcomeNoVerdict, got %q", out)
	}
	blocked := 0
	for _, res := range gw.results {
		if res.Result == "blocked" {
			blocked++
		}
	}
	if blocked != 0 {
		t.Errorf("a stale pre-success streak leaked into a block: %d block(s) reported", blocked)
	}
}
