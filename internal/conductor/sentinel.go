package conductor

import (
	"context"
	"time"

	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/events"
	"github.com/everva/conductor-platform/internal/sentinel"
	"github.com/everva/conductor-platform/internal/statestore"
)

// SentinelDecider is the OPTIONAL progress-watchdog seam (ADR-0006 3-layer
// sentinel, Faz-2 2C-1). While a task's develop is in flight, the tick runs a
// WATCHER that periodically builds liveness Signals and asks the decider to
// Assess them; a Kill or Escalate decision cancels the develop child context
// (killing the performer process group via exec.CommandContext + Setpgid, the
// SAME mechanism as the F-2 abort watcher), routing the tick to a sentinel-killed
// block (Kill) or an intervention-needed escalation (Escalate). A Continue keeps
// the develop running until the existing develop timeout (the daemon's -timeout,
// which is the deterministic Layer-3 backstop at the process level).
//
// It is narrowed to Assess so *sentinel.Sentinel satisfies it directly and a fake
// can drive the tick deterministically. The field is OPTIONAL on Deps — a nil
// Sentinel means no watchdog runs (Layer-1+3-only, today's behavior), so existing
// develop/e2e tests are unaffected.
type SentinelDecider interface {
	Assess(ctx context.Context, sig sentinel.Signals) sentinel.Result
}

// ProgressProbe supplies the deterministic liveness signals the sentinel watchdog
// reasons over for a running develop. It is the seam that decouples the watchdog
// from HOW liveness is observed: the default production probe (engineProgressProbe)
// derives signals from the engine's Health (ADR-0006 Layer-1: LastActivityTS +
// the progressing/idle signal) plus the elapsed wall time; a test injects a
// scripted probe so a stalled/dead/progressing run is modelled without a real
// subprocess.
//
// Probe is called on each watchdog tick with the wall time the develop started;
// it returns the Signals for THIS moment. A probe error is treated by the watcher
// as "no usable signal this tick" (skip), so a transient Health failure never
// spuriously kills a healthy develop.
type ProgressProbe interface {
	Probe(ctx context.Context, startedAt time.Time, now time.Time) (sentinel.Signals, error)
}

// ProgressProbeFunc adapts a function to ProgressProbe.
type ProgressProbeFunc func(ctx context.Context, startedAt time.Time, now time.Time) (sentinel.Signals, error)

// Probe calls the underlying function.
func (f ProgressProbeFunc) Probe(ctx context.Context, startedAt, now time.Time) (sentinel.Signals, error) {
	return f(ctx, startedAt, now)
}

// engine Health Signal values (ADR-0006 Layer-1; mirrors CommandEngine.Health):
// the coarse liveness verdict the probe maps onto Signals. They are mirrored by
// value (not imported) since the engine exposes them only as the string field.
const (
	// signalProgressing means output flowed recently (within the engine's idle
	// threshold): the performer is actively working.
	signalProgressing = "progressing"
	// signalIdle means the performer process is up but output has gone quiet past
	// the engine's idle threshold: a stalled run (no activity), which the sentinel
	// must reason about via the gray-zone/backstop path rather than ignore.
	signalIdle = "idle"
	// signalUnknown means the engine has observed NO activity yet (a freshly started
	// performer that has not emitted): alive, not dead.
	signalUnknown = "unknown"
)

// engineProgressProbe is the default ProgressProbe: it builds Signals from the
// engine's deterministic Health (Layer-1) and the elapsed wall time. Elapsed is
// now-startedAt; SinceActivity is now-LastActivityTS (or the full elapsed if the
// engine has reported no activity yet). It carries no LLM logic — the gray-zone
// advisor is the sentinel's job, fed LastOutput which the engine Health does not
// expose here, so LastOutput is left empty (a CommandAdvisor still gets the prompt
// scaffold; a richer probe can fill it in Faz-2b without changing this seam).
//
// Alive (L1 wiring): the engine's Health Signal IS wired through rather than
// hardcoded true, so Layer-1 is not inert in production and the watchdog reasons
// over the engine's real coarse verdict. The mapping is deliberately CONSERVATIVE
// because the engine's Health has NO terminal "process gone" value (it only reports
// progressing/idle/unknown from observed OUTPUT, not process liveness):
//
//   - "progressing" / "unknown" (and any unrecognized value): Alive=true. A
//     freshly-started performer that has not emitted yet ("unknown") is not dead.
//   - "idle" (process up, but output stalled past the engine's idle threshold):
//     Alive=true STILL, because output stalling is NOT proof the process died — a
//     long compile/test legitimately produces no output for a while. Forcing
//     Alive=false here would make Layer-1 KILL such a run WITHOUT the advisor (a
//     false-positive kill), which the sentinel's design forbids. So "idle" is
//     reported as alive-but-stalled, which — together with the SinceActivity
//     staleness the SAME Health gives us — routes a hung-no-output performer into
//     the sentinel's GRAY ZONE (advisor) and ultimately the deterministic Layer-3
//     backstop, the authoritative bound on a truly hung run.
//
// Because of this (the engine cannot prove a process dead from output alone), the
// Layer-1 "clearly dead -> Kill" path is NOT driven from this default probe; that
// claim is softened in the sentinel docs accordingly. Alive=false is reserved for
// a future richer probe that can observe actual process liveness without changing
// this seam (it would then flow straight into Layer-1's clearly-dead branch).
type engineProgressProbe struct {
	eng     engine.EngineAdapter
	session engine.Session
}

// Probe reads the engine Health and maps it onto Signals, wiring the coarse
// Health.Signal through to Alive (L1) so the watchdog is not inert in production.
func (p engineProgressProbe) Probe(ctx context.Context, startedAt, now time.Time) (sentinel.Signals, error) {
	hs, err := p.eng.Health(ctx, p.session)
	if err != nil {
		return sentinel.Signals{}, err
	}
	elapsed := now.Sub(startedAt)
	sinceActivity := elapsed
	if !hs.LastActivityTS.IsZero() {
		sinceActivity = now.Sub(hs.LastActivityTS)
	}
	return sentinel.Signals{
		Elapsed:       elapsed,
		SinceActivity: sinceActivity,
		Alive:         aliveFromSignal(hs.Signal),
	}, nil
}

// aliveFromSignal maps the engine's coarse Health.Signal (L1) onto the sentinel's
// Alive axis. It is conservative by design (see engineProgressProbe): the engine
// reports output-derived liveness only, never proven process death, so every known
// Signal — including a stalled "idle" — is reported alive, deferring a stalled run
// to the gray-zone advisor + Layer-3 backstop rather than a Layer-1 false-kill. It
// is a named function (not an inline literal) so the wiring is unit-testable per
// Signal value.
func aliveFromSignal(signal string) bool {
	switch signal {
	case signalProgressing, signalUnknown, signalIdle, "":
		return true
	default:
		// An unrecognized Signal is treated conservatively as alive: never kill on a
		// value the contract does not define; the backstop still bounds the run.
		return true
	}
}

// developWithSentinel runs the engine's Develop under BOTH the F-2 abort watcher
// AND the 2C-1 sentinel progress-watchdog, using the SAME child-context cancel
// machinery so a cancel from either source kills the performer process group. It
// returns the develop verdict/error, the abort flag (operator abort), and the
// sentinel Result that caused a cancel (zero-value Decision when the sentinel did
// not fire).
//
// Precedence when both watchers could fire: whichever cancels FIRST wins; the
// abort flag and the sentinel result are read race-free after the watchers join.
// If the abort watcher fired, `aborted` is true and the caller routes to the safe
// revert (abort takes priority over a sentinel decision, since an operator abort
// is an explicit human action). Otherwise a non-zero sentinel decision routes to
// the sentinel handler.
//
// With BOTH a nil Aborter and a nil Sentinel this degrades to a plain develop
// under ctx (the pre-2C-1, pre-F-2 behavior), so existing wiring and tests are
// unaffected. With only one injected, only that watcher runs.
func (c *Conductor) developWithSentinel(ctx context.Context, task statestore.Task, ws engine.Workspace) (engine.Verdict, bool, sentinel.Result, error) {
	// Fast path: no watchers at all -> plain develop (unchanged behavior).
	if c.aborter == nil && c.sentinel == nil {
		v, err := c.engine.Develop(ctx, task, ws)
		return v, false, sentinel.Result{}, err
	}

	devCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	poll := c.sentinelPoll
	if poll <= 0 {
		poll = c.abortPoll
	}
	if poll <= 0 {
		poll = defaultAbortPoll
	}

	// aborted + sentResult are written ONLY by the watcher goroutine and read ONLY
	// after it has exited (the <-watcherDone join), so the channel close establishes
	// the happens-before edge and no mutex is needed.
	var (
		aborted    bool
		sentResult sentinel.Result
	)
	probe := c.progressProbe(task)
	startedAt := c.now()

	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		ticker := time.NewTicker(poll)
		defer ticker.Stop()
		for {
			select {
			case <-devCtx.Done():
				return
			case <-ticker.C:
				// Abort takes priority: an explicit operator action.
				if c.aborter != nil {
					if req, err := c.aborter.AbortRequested(devCtx, task.ID); err == nil && req {
						aborted = true
						cancel()
						return
					}
				}
				// Sentinel progress-watchdog (2C-1).
				if c.sentinel != nil && probe != nil {
					sig, err := probe.Probe(devCtx, startedAt, c.now())
					if err != nil {
						continue // no usable signal this tick; never spuriously kill.
					}
					res := c.sentinel.Assess(devCtx, sig)
					if res.Decision == sentinel.Kill || res.Decision == sentinel.Escalate {
						sentResult = res
						cancel()
						return
					}
				}
			}
		}
	}()

	v, err := c.engine.Develop(devCtx, task, ws)
	cancel()
	<-watcherDone
	return v, aborted, sentResult, err
}

// progressProbe returns the ProgressProbe for this task: the injected probe if a
// test supplied one, else the default engine-Health-derived probe. It returns nil
// only when no sentinel is wired (the watcher then skips the sentinel branch).
func (c *Conductor) progressProbe(task statestore.Task) ProgressProbe {
	if c.sentinel == nil {
		return nil
	}
	if c.probe != nil {
		return c.probe
	}
	return engineProgressProbe{eng: c.engine, session: engine.Session{TaskID: task.ID}}
}

// now returns the current time via the injectable clock (defaulting to time.Now)
// so the watchdog's elapsed/since computations are deterministic in tests.
func (c *Conductor) now() time.Time {
	if c.clock != nil {
		return c.clock()
	}
	return time.Now()
}

// handleSentinelDecision routes a sentinel-triggered develop cancellation to the
// right terminal outcome (ADR-0006). A Kill blocks the task with a sentinel-killed
// reason and emits an intervention-needed event (the deterministic stuck/dead/
// backstop verdict — never fake-green). An Escalate also cancels and blocks but
// signals that a HUMAN is needed (the gray-zone advisor's needs_human verdict).
// Both leave NO verify and NO merge for this run, so the base branch gets NO
// [task:<id>] trailer.
func (c *Conductor) handleSentinelDecision(ctx context.Context, task statestore.Task, res sentinel.Result) (TickResult, error) {
	reason := "sentinel-killed"
	if res.Decision == sentinel.Escalate {
		reason = "sentinel-escalate (human needed)"
	}
	c.emit(ctx, task, events.PhaseDevelop, events.KindInterventionNeeded, map[string]any{
		"reason": reason,
		"layer":  res.Layer,
		"advice": string(res.Advice),
		"detail": res.Reason,
	})
	if err := c.markBlocked(ctx, task); err != nil {
		return TickResult{}, err
	}
	return TickResult{Outcome: OutcomeSentinelKilled, TaskID: task.ID}, nil
}
