package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/everva/conductor-platform/internal/agentclient"
)

// fakeGateway scripts the gateway agent-API for Runner tests.
type fakeGateway struct {
	leaseTask      *agentclient.LeasedTask // nil → no work
	scenarios      []agentclient.ScenarioInfo
	resultDecision string
	decisionStates []string // returned in sequence by Decision

	// recorded
	decisionIdx  int
	resultReport agentclient.ResultReport
	mergedSHA    string
	released     bool
	reports      int

	// mu guards the recorded fields against the runner's concurrent progress heartbeat.
	mu             sync.Mutex
	progressPhases []string // phases of every kind=="progress" report
}

func (f *fakeGateway) Lease(_ context.Context, _, _ string, _ []string) (agentclient.LeasedTask, bool, error) {
	if f.leaseTask == nil {
		return agentclient.LeasedTask{}, false, nil
	}
	return *f.leaseTask, true, nil
}
func (f *fakeGateway) Scenarios(_ context.Context, _ string) ([]agentclient.ScenarioInfo, error) {
	return f.scenarios, nil
}
func (f *fakeGateway) Report(_ context.Context, _, _, phase, kind string, _ map[string]any) error {
	f.mu.Lock()
	f.reports++
	if kind == "progress" {
		f.progressPhases = append(f.progressPhases, phase)
	}
	f.mu.Unlock()
	return nil
}
func (f *fakeGateway) Result(_ context.Context, _, _ string, report agentclient.ResultReport) (string, error) {
	f.resultReport = report
	return f.resultDecision, nil
}
func (f *fakeGateway) Decision(_ context.Context, _, _ string) (string, error) {
	i := f.decisionIdx
	if i >= len(f.decisionStates) {
		i = len(f.decisionStates) - 1
	}
	f.decisionIdx++
	return f.decisionStates[i], nil
}
func (f *fakeGateway) Merged(_ context.Context, _, _, sha string) error {
	f.mergedSHA = sha
	return nil
}
func (f *fakeGateway) Release(_ context.Context, _, _, _ string) error { f.released = true; return nil }
func (f *fakeGateway) Heartbeat(_ context.Context, _ string) error     { return nil }

// fakeExecutor scripts the local develop/verify/merge for Runner tests.
type fakeExecutor struct {
	out      RunOutcome
	runErr   error
	mergeSHA string
	mergeErr error

	// recorded
	mergeApproved bool
	merges        int
	cleaned       bool
	runs          int
}

func (f *fakeExecutor) Run(_ context.Context, _ agentclient.TaskInfo, _ agentclient.ScenarioInfo) (RunOutcome, error) {
	f.runs++
	return f.out, f.runErr
}
func (f *fakeExecutor) Merge(_ context.Context, _ agentclient.TaskInfo, _ string, approved bool) (string, error) {
	f.merges++
	f.mergeApproved = approved
	return f.mergeSHA, f.mergeErr
}
func (f *fakeExecutor) Cleanup(_ context.Context, _ agentclient.TaskInfo) { f.cleaned = true }

func leased() *agentclient.LeasedTask {
	return &agentclient.LeasedTask{Task: agentclient.TaskInfo{ID: "T-1", ProjectID: "p", Tier: "T2"}}
}

func newRunner(gw Gateway, ex Executor) *Runner {
	return New(gw, ex, Config{ProjectID: "p", HostID: "davinci", PollInterval: time.Millisecond})
}

func TestRunOnce_NoWork(t *testing.T) {
	gw := &fakeGateway{leaseTask: nil}
	out, err := newRunner(gw, &fakeExecutor{}).RunOnce(context.Background())
	if err != nil || out != OutcomeNoWork {
		t.Fatalf("out=%q err=%v, want no-work", out, err)
	}
}

func TestRunOnce_AutoMerge(t *testing.T) {
	gw := &fakeGateway{leaseTask: leased(), resultDecision: "merge"}
	ex := &fakeExecutor{out: RunOutcome{Result: "pass", Branch: "conductor/p/T-1"}, mergeSHA: "9f2c1ab"}
	out, err := newRunner(gw, ex).RunOnce(context.Background())
	if err != nil || out != OutcomeMerged {
		t.Fatalf("out=%q err=%v, want merged", out, err)
	}
	if ex.merges != 1 || ex.mergeApproved {
		t.Fatalf("auto-merge should merge once, not approved-mode: merges=%d approved=%v", ex.merges, ex.mergeApproved)
	}
	if gw.mergedSHA != "9f2c1ab" || !gw.released || !ex.cleaned {
		t.Fatalf("merged sha/release/cleanup not recorded: %+v cleaned=%v", gw, ex.cleaned)
	}
}

func TestRunOnce_HeldThenApproved(t *testing.T) {
	gw := &fakeGateway{leaseTask: leased(), resultDecision: "hold", decisionStates: []string{"pending", "approved"}}
	ex := &fakeExecutor{out: RunOutcome{Result: "pass", Branch: "conductor/p/T-1"}, mergeSHA: "abc"}
	out, err := newRunner(gw, ex).RunOnce(context.Background())
	if err != nil || out != OutcomeMerged {
		t.Fatalf("out=%q err=%v, want merged", out, err)
	}
	if !ex.mergeApproved {
		t.Fatalf("approved held task must merge in approved mode (re-verify)")
	}
	if gw.mergedSHA != "abc" || !gw.released {
		t.Fatalf("approved merge not reported/released")
	}
}

func TestRunOnce_HeldThenAborted(t *testing.T) {
	gw := &fakeGateway{leaseTask: leased(), resultDecision: "hold", decisionStates: []string{"aborted"}}
	ex := &fakeExecutor{out: RunOutcome{Result: "pass", Branch: "b"}}
	out, err := newRunner(gw, ex).RunOnce(context.Background())
	if err != nil || out != OutcomeAborted {
		t.Fatalf("out=%q err=%v, want aborted", out, err)
	}
	if ex.merges != 0 {
		t.Fatalf("aborted held task must NOT merge")
	}
	if !gw.released || !ex.cleaned {
		t.Fatalf("aborted task must release + cleanup")
	}
}

func TestRunOnce_Blocked(t *testing.T) {
	gw := &fakeGateway{leaseTask: leased(), resultDecision: "blocked"}
	ex := &fakeExecutor{out: RunOutcome{Result: "changes-requested", Summary: "gate failed"}}
	out, err := newRunner(gw, ex).RunOnce(context.Background())
	if err != nil || out != OutcomeBlocked {
		t.Fatalf("out=%q err=%v, want blocked", out, err)
	}
	if ex.merges != 0 || !gw.released {
		t.Fatalf("blocked task must not merge, must release")
	}
}

func TestRunOnce_RunError_ReportsBlocked(t *testing.T) {
	gw := &fakeGateway{leaseTask: leased(), resultDecision: "blocked"}
	ex := &fakeExecutor{runErr: errors.New("provision exploded")}
	out, err := newRunner(gw, ex).RunOnce(context.Background())
	if err != nil || out != OutcomeBlocked {
		t.Fatalf("out=%q err=%v, want blocked (never fake-green)", out, err)
	}
	// The reported verdict must be blocked, never a fabricated pass.
	if gw.resultReport.Result != "blocked" {
		t.Fatalf("run error reported as %q, want blocked", gw.resultReport.Result)
	}
	if !gw.released {
		t.Fatalf("run error must still release the lease")
	}
}

func TestRunOnce_AlwaysReleasesOnMergeError(t *testing.T) {
	gw := &fakeGateway{leaseTask: leased(), resultDecision: "merge"}
	ex := &fakeExecutor{out: RunOutcome{Result: "pass", Branch: "b"}, mergeErr: errors.New("merge conflict")}
	_, err := newRunner(gw, ex).RunOnce(context.Background())
	if err == nil {
		t.Fatalf("expected a merge error")
	}
	if !gw.released || !ex.cleaned {
		t.Fatalf("a merge error must STILL release the lease + cleanup (defer)")
	}
}

// progressExecutor implements ProgressProbe + a Run that blocks long enough for the heartbeat
// to tick, so the progress-pulse path is exercised.
type progressExecutor struct {
	fakeExecutor
}

func (p *progressExecutor) Run(ctx context.Context, t agentclient.TaskInfo, s agentclient.ScenarioInfo) (RunOutcome, error) {
	time.Sleep(25 * time.Millisecond) // allow ≥1 progress tick at ProgressInterval=5ms
	return p.fakeExecutor.Run(ctx, t, s)
}

func (p *progressExecutor) Progress(_ string) (string, int) { return "developing", 3 }

// TestRunOnce_EmitsProgressPulse proves the runner emits a live progress pulse (kind=="progress"
// with the executor's phase) DURING Run, feeding the editor's "Now" view.
func TestRunOnce_EmitsProgressPulse(t *testing.T) {
	gw := &fakeGateway{leaseTask: leased(), resultDecision: "blocked"}
	ex := &progressExecutor{fakeExecutor{out: RunOutcome{Result: "blocked"}}}
	r := New(gw, ex, Config{ProjectID: "p", HostID: "davinci", PollInterval: time.Millisecond, ProgressInterval: 5 * time.Millisecond})

	if _, err := r.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}

	gw.mu.Lock()
	phases := append([]string(nil), gw.progressPhases...)
	gw.mu.Unlock()
	if len(phases) == 0 {
		t.Fatalf("expected ≥1 progress pulse during develop, got none")
	}
	for _, ph := range phases {
		if ph != "developing" {
			t.Fatalf("progress phase = %q, want developing", ph)
		}
	}
}
