// Package agent is the host-agent execution loop (ADR-0048, Faz A): the gateway-
// mediated analogue of the daemon's Conductor.Tick. It leases a task over HTTP,
// develops + verifies it locally (reusing the engine/verify/merger packages via the
// Executor seam), reports the verdict to the gateway, and — honoring held-for-review —
// either merges (auto) or waits for the director's approval before merging. It NEVER
// touches Postgres: every piece of state crosses the Gateway HTTP seam.
//
// The Runner orchestrates over two seams so the loop logic is testable without a real
// gateway, a real LLM, or real git:
//
//   - Gateway: the conductor-api agent-API (agentclient.Client satisfies it).
//   - Executor: provision+develop+verify (Run), squash-merge (Merge), worktree cleanup.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/everva/conductor-platform/internal/agentclient"
	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/events"
)

// Gateway is the conductor-api agent-API surface the Runner consumes. *agentclient.Client
// satisfies it; tests inject a fake.
type Gateway interface {
	Lease(ctx context.Context, projectID, hostID string, capabilities []string) (agentclient.LeasedTask, bool, error)
	Scenarios(ctx context.Context, projectID string) ([]agentclient.ScenarioInfo, error)
	Report(ctx context.Context, projectID, taskID, phase, kind string, payload map[string]any) error
	Result(ctx context.Context, projectID, taskID string, report agentclient.ResultReport) (string, error)
	// StoreTaskDiff persists the FULL-file patch so GET .../tasks/{task}/diff serves the native
	// side-by-side diff for review (Faz-S review parity). Best-effort (observability).
	StoreTaskDiff(ctx context.Context, projectID, taskID string, body agentclient.TaskDiffBody) error
	Decision(ctx context.Context, projectID, taskID string) (string, error)
	Merged(ctx context.Context, projectID, taskID, sha string) error
	Release(ctx context.Context, projectID, hostID, taskID string) error
	Heartbeat(ctx context.Context, hostID string) error
}

// RunOutcome is the result of an Executor.Run (provision+develop+verify).
type RunOutcome struct {
	// Result is the gate verdict: pass | changes-requested | blocked.
	Result string
	// Branch is the verified per-task branch (recorded if the task is held).
	Branch string
	// Summary is a short, secret-free description for the verdict event.
	Summary string
	// Checks are the individual gate checks (for the KindDecision event).
	Checks []agentclient.Check
	// Findings are the reviewer/gate's unresolved findings when the verdict is not pass. The gateway
	// PERSISTS them onto the scenario's acceptance (deduped) so a later re-develop — a fresh worktree
	// that loses .conductor/REVIEW.md — still addresses them and the reviewer re-checks them. This is
	// what lets a dense screen converge across autoheal retries instead of cycling forever.
	Findings []string
	// Diff is the BOUNDED branch-vs-base summary (events.DiffSummary) the agent publishes as the
	// KindDiff event so the director SEES the change in the Session view + board (review parity with
	// the in-process daemon). nil when the diff couldn't be computed (observability-only — the run
	// still reports its verdict).
	Diff *events.DiffSummary
	// FullPatch is the whole-file unified patch for the stored TaskDiff (native side-by-side diff);
	// "" when none/failed. FullTruncated marks a budget cap.
	FullPatch     string
	FullTruncated bool
}

// MergeResult is the outcome of Executor.Merge: the squash-merge SHA plus the full-file diff
// computed from the verified, base-merged worktree at the branch tip. The runner persists this as a
// TaskDiff on EVERY merge path (auto + approved-held) so GET .../tasks/{task}/diff always serves a
// full-file native diff — not only when the once-per-Run early emit (RunOnce) happened to capture it
// (that misses the held→approved→merge and re-verify paths — exactly the big multi-file tasks).
type MergeResult struct {
	SHA       string
	Base      string
	Branch    string
	Patch     string
	Truncated bool
}

// Executor runs the local, git-and-LLM-native half of a task. The real adapter wires
// the provisioner + engine + verify + merger; tests inject a fake.
type Executor interface {
	// Run provisions a worktree, develops (the performer), and verifies (the gate),
	// returning the outcome. It holds the worktree for a later Merge/Cleanup.
	Run(ctx context.Context, task agentclient.TaskInfo, scenario agentclient.ScenarioInfo) (RunOutcome, error)
	// Merge squash-merges the verified branch and returns the merge SHA. When approved
	// is true (a held task the director approved) it re-verifies against the current
	// base first (drift guard) before merging.
	Merge(ctx context.Context, task agentclient.TaskInfo, scenario agentclient.ScenarioInfo, branch string, approved bool) (MergeResult, error)
	// Cleanup removes the task's worktree (best-effort).
	Cleanup(ctx context.Context, task agentclient.TaskInfo)
}

// ProgressProbe is an OPTIONAL Executor capability: it reports the current phase
// (provisioning|developing|verifying), how many files the performer has changed so far, and a
// rich human-readable activity line (📖/✍️/🔎/🤔 — what the performer is doing right now), so the
// runner can emit a live progress PULSE during the long, otherwise-silent develop/verify phases —
// feeding the editor's session timeline + a liveness signal so the director is never blind. detail
// is "" when nothing is surfaced (the pulse still fires). RealExecutor implements it; an executor
// that doesn't simply gets no pulse (RunOnce still works).
type ProgressProbe interface {
	Progress(taskID string) (phase string, filesChanged int, detail string)
}

// progressInterval is how often the develop/verify heartbeat emits a progress event.
const progressInterval = 20 * time.Second

// eventPhase maps the executor's granular step (provisioning|developing|verifying) to a VALID
// events.Phase ("plan"|"develop"|"verify") — the gateway's Event.Validate rejects any other
// phase, which would silently drop the progress pulse. The granular step is preserved in the
// payload ("step") for the editor's "Now" sentence.
func eventPhase(step string) string {
	switch step {
	case "provisioning":
		return "plan"
	case "verifying":
		return "verify"
	case "reviewing":
		return "review"
	default:
		return "develop"
	}
}

// Config configures a Runner.
type Config struct {
	ProjectID    string
	HostID       string
	Capabilities []string
	// PollInterval is how often a held task's decision is polled. Defaults to 15s.
	PollInterval time.Duration
	// ProgressInterval is how often the develop/verify "Now" pulse is emitted. Defaults to 20s.
	ProgressInterval time.Duration
	// RateLimitBackoff is how long the loop sleeps after the performer hits its rolling
	// usage limit (ErrRateLimited) before attempting the next lease. Defaults to 10m.
	RateLimitBackoff time.Duration
	Logger           *slog.Logger
}

// statusAwaitingApproval is the held-for-review task status as it crosses the gateway HTTP
// seam (registry.StatusAwaitingApproval). The agent compares the string rather than importing
// the store-side registry package: it never touches Postgres (ADR-0048).
const statusAwaitingApproval = "awaiting-approval"

// MaxMergeAttempts caps how many CONSECUTIVE times one task may fail its squash-merge
// before the agent stops retrying and reports it blocked (with the git error as the
// board's LastError).
//
// A merge failure reports no verdict, so the deferred lease-release reverts the task
// running→ready (gateway M1) and the next tick picks it straight back up. That is
// deliberate — the usual cause is that the base advanced under the branch, and one
// re-cut-from-base + re-develop resolves it. But when the conflict is INHERENT to the
// branch (e.g. its merge-base tracks a file the base later deleted) every retry repeats
// it identically: xirigo-vendor's V-44 failed the same `CONFLICT (modify/delete)` four
// times in 50 minutes and would have looped forever, burning a shared host's CPU on a
// wall no retry can clear. One free self-heal retry, then block for the director.
const MaxMergeAttempts = 2

// MaxNoVerdictAttempts caps how many CONSECUTIVE runs of ONE task may end with NO VERDICT before
// the agent finally reports it blocked.
//
// A run that produces no verdict produces no EVIDENCE. The performer never judged the code — it
// was walled by a usage limit, killed by the host's OOM/swap, cut off by a DNS blip, or timed out.
// Reporting that as `blocked` tells the director "this task needs your review", which is a lie:
// there is nothing to review. It also POISONS the queue — on 2026-07-11 a weekly usage limit turned
// 21 backend tasks into `blocked` in half an hour, all with the same summary ("malformed verdict:
// no result-keyed JSON object in output"), none of them a defect. The board said 25 need-review;
// 21 of them were the wall, not the work.
//
// So: no verdict ⇒ no blocking. The task goes back to ready (the deferred lease-release reverts
// running→ready) and is retried. Only when ONE task keeps producing nothing WHILE OTHER TASKS ARE
// FINE — i.e. the environment is demonstrably healthy and this task alone is unworkable — is a
// block honest, and then it carries the performer's actual last words as evidence.
const MaxNoVerdictAttempts = 3

// EnvSuspectTasks is how many DISTINCT tasks must fail with no verdict, back to back, before the
// agent concludes the fault is the ENVIRONMENT rather than any task.
//
// This is the guard that makes MaxNoVerdictAttempts safe. Without it, a walled account would simply
// burn every task's 3 attempts and block them all anyway — the same 21 fake reviews, three times
// slower. Two different tasks failing to produce a verdict in a row is not a coincidence about the
// code; it is a statement about the machine or the account. In that state the agent blocks NOTHING
// and backs off, leaving the queue intact for when the wall clears.
const EnvSuspectTasks = 2

// noVerdictBackoff is the short pause after a SINGLE no-verdict run. The task went straight back to
// ready, so without a pause the next lease re-picks it instantly and spins against whatever just
// broke. Long enough to let a blip pass, short enough that a healthy fleet loses no throughput.
const noVerdictBackoff = 90 * time.Second

// Runner is the host-agent's one-task orchestrator.
type Runner struct {
	gw          Gateway
	ex          Executor
	cfg         Config
	log         *slog.Logger
	poll        time.Duration
	progress    time.Duration
	rateBackoff time.Duration

	// mergeFails counts CONSECUTIVE merge failures per task ID (reset on success).
	// Guarded because a Runner may legitimately be shared across goroutines even though
	// Loop drives RunOnce sequentially.
	mu         sync.Mutex
	mergeFails map[string]int

	// noVerdict counts CONSECUTIVE no-verdict runs per task ID, and noVerdictTasks holds the
	// DISTINCT tasks in the current no-verdict streak. Both reset the moment any run produces a
	// verdict — that success is the proof the environment works. Together they answer the only
	// question that matters when a run comes back empty: is this task broken, or is the machine?
	noVerdict      map[string]int
	noVerdictTasks map[string]struct{}
}

// Outcome classifies what RunOnce did, for the loop + logging.
type Outcome string

const (
	OutcomeNoWork  Outcome = "no-work"
	OutcomeMerged  Outcome = "merged"
	OutcomeHeld    Outcome = "held" // returned only when the held poll is interrupted (ctx done)
	OutcomeBlocked Outcome = "blocked"
	OutcomeAborted Outcome = "aborted"
	// OutcomeRateLimited: the performer's account hit its rolling usage limit. The task was
	// released WITHOUT a verdict (reverts running→ready, gateway M1) so it is re-picked after
	// the window resets; the Loop backs off (RateLimitBackoff) before the next lease.
	OutcomeRateLimited Outcome = "rate-limited"
	// OutcomeNoVerdict: the run produced no verdict at all, so it produced no evidence. The task was
	// released WITHOUT a verdict (reverts running→ready) and will be retried; nothing is blocked. The
	// Loop backs off — briefly for a one-off, long once the environment itself looks suspect.
	OutcomeNoVerdict Outcome = "no-verdict"
)

// New returns a Runner over the gateway + executor.
func New(gw Gateway, ex Executor, cfg Config) *Runner {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	poll := cfg.PollInterval
	if poll <= 0 {
		poll = 15 * time.Second
	}
	prog := cfg.ProgressInterval
	if prog <= 0 {
		prog = progressInterval
	}
	rateBackoff := cfg.RateLimitBackoff
	if rateBackoff <= 0 {
		rateBackoff = 10 * time.Minute
	}
	return &Runner{gw: gw, ex: ex, cfg: cfg, log: log, poll: poll, progress: prog, rateBackoff: rateBackoff,
		mergeFails: map[string]int{}, noVerdict: map[string]int{}, noVerdictTasks: map[string]struct{}{}}
}

// RunOnce leases at most one task and drives it to a terminal outcome (merged,
// blocked, aborted) or returns no-work. The lease is ALWAYS released (defer), so a
// crash mid-task frees the repo for the reaper. Observability reports are best-effort.
func (r *Runner) RunOnce(ctx context.Context) (Outcome, error) {
	lt, ok, err := r.gw.Lease(ctx, r.cfg.ProjectID, r.cfg.HostID, r.cfg.Capabilities)
	if err != nil {
		return "", fmt.Errorf("agent: lease: %w", err)
	}
	if !ok {
		return OutcomeNoWork, nil
	}
	task := lt.Task

	// Always release the lease, on every path. Cleanup the worktree too.
	defer func() {
		r.ex.Cleanup(context.WithoutCancel(ctx), task)
		if rerr := r.gw.Release(context.WithoutCancel(ctx), r.cfg.ProjectID, r.cfg.HostID, task.ID); rerr != nil {
			r.log.Warn("agent: lease release failed", "task", task.ID, "err", rerr)
		}
	}()

	scenario := r.scenarioFor(ctx, task)

	// RESUME AN APPROVED MERGE. The gateway hands back an awaiting-approval task only once the
	// director approved it (registry.isPickable). Its work is already developed and gate-verified
	// and its branch is preserved, so merge it — do NOT re-develop, which would cut a fresh branch
	// from the base and discard the verified commit. This is what makes an approval durable: the
	// fast path is the agent still polling /decision, but if that process restarted, hit its usage
	// limit, or crashed, ANY later agent finishes the merge instead of the task being stranded.
	if task.Status == statusAwaitingApproval && task.Approved && task.Branch != "" {
		r.log.Info("agent: resuming approved merge for a previously held task", "task", task.ID, "branch", task.Branch)
		r.report(ctx, task.ID, "review", "approved-merge-resume", map[string]any{"task": task.ID, "branch": task.Branch})
		return r.merge(ctx, task, scenario, task.Branch, true)
	}

	r.report(ctx, task.ID, "develop", "started", map[string]any{"task": task.ID})

	out, err := r.runWithProgress(ctx, task, scenario)
	if err != nil {
		if errors.Is(err, engine.ErrRateLimited) {
			// The performer's Claude account hit its rolling usage limit — a wall only TIME clears.
			// Return WITHOUT reporting a verdict: the deferred lease-release then reverts the task
			// running→ready (gateway M1), so it is re-picked after the window resets WITHOUT churning
			// it into blocked or burning the transient-retry budget. The Loop backs off before the
			// next lease. Best-effort observability so the board/journal shows why the agent idled.
			r.log.Warn("agent: performer rate-limited; releasing lease (task→ready), backing off", "task", task.ID, "err", err)
			r.report(ctx, task.ID, "develop", "rate-limited", map[string]any{"task": task.ID, "error": err.Error()})
			return OutcomeRateLimited, nil
		}
		// NO VERDICT ⇒ NO EVIDENCE ⇒ NO REVIEW. Reaching here means the pipeline broke before the
		// performer judged anything: it was walled, killed, timed out or could not even provision.
		// This used to be reported as `blocked`, which put the task in the director's review queue
		// under a summary that describes the harness ("malformed verdict: no result-keyed JSON
		// object in output"), not the code. It is not a defect and it is not reviewable.
		//
		// Decide which of two very different things just happened, and never guess:
		//   * the ENVIRONMENT is down (account walled, host thrashing, DNS gone) — then more than
		//     one task fails this way back to back, and NOTHING may be blocked; back off and wait.
		//   * THIS task alone cannot produce a verdict while others are landing fine — then, and
		//     only then, block it, carrying the performer's actual last words as the evidence.
		r.mu.Lock()
		r.noVerdict[task.ID]++
		r.noVerdictTasks[task.ID] = struct{}{}
		perTask, distinct := r.noVerdict[task.ID], len(r.noVerdictTasks)
		r.mu.Unlock()

		envSuspect := distinct >= EnvSuspectTasks
		switch {
		case envSuspect:
			r.log.Warn("agent: no verdict, and it is not the task — the environment is failing; blocking NOTHING",
				"task", task.ID, "distinct_tasks_failing", distinct, "err", err)
			r.report(ctx, task.ID, "develop", "no-verdict", map[string]any{
				"task": task.ID, "error": err.Error(), "distinct_tasks_failing": distinct,
				"verdict": "environment suspected — task returned to ready, nothing blocked",
			})
			return OutcomeNoVerdict, nil
		case perTask < MaxNoVerdictAttempts:
			r.log.Warn("agent: no verdict; returning the task to ready and retrying (not a review item)",
				"task", task.ID, "attempt", perTask, "of", MaxNoVerdictAttempts, "err", err)
			r.report(ctx, task.ID, "develop", "no-verdict", map[string]any{
				"task": task.ID, "error": err.Error(), "attempt": perTask, "max": MaxNoVerdictAttempts,
			})
			return OutcomeNoVerdict, nil
		default:
			// Earned: this task, and only this task, has come back empty MaxNoVerdictAttempts times
			// in a row while the environment kept working. Now a human should look — and the summary
			// says what the performer actually said, not what the parser wished for.
			r.log.Warn("agent: this task alone never produces a verdict; reporting blocked",
				"task", task.ID, "attempts", perTask, "err", err)
			out = RunOutcome{Result: "blocked", Summary: fmt.Sprintf(
				"no verdict in %d consecutive runs while other tasks succeeded — the developer never judged this task: %s",
				perTask, err.Error())}
		}
	} else {
		// A verdict exists. Whatever it says, the pipeline worked — which is the proof that the
		// environment is healthy. Clear both counters so an old wall cannot leak into a later block.
		r.mu.Lock()
		delete(r.noVerdict, task.ID)
		r.noVerdictTasks = map[string]struct{}{}
		r.mu.Unlock()
	}
	// Review parity: publish the branch-vs-base diff so the director can SEE the change before
	// approving. The bounded summary rides a KindDiff event (Session timeline + board diff size);
	// the full-file patch is stored so the native side-by-side diff (GET .../diff) works. Both
	// best-effort — a diff failure never sinks the verdict report below.
	if out.Diff != nil {
		r.report(ctx, task.ID, "review", "diff", out.Diff.Payload())
		if out.FullPatch != "" {
			_ = r.gw.StoreTaskDiff(context.WithoutCancel(ctx), r.cfg.ProjectID, task.ID, agentclient.TaskDiffBody{
				Base: out.Diff.Base, Branch: out.Diff.Branch, Patch: out.FullPatch, Truncated: out.FullTruncated,
			})
		}
	}

	decision, err := r.gw.Result(ctx, r.cfg.ProjectID, task.ID, agentclient.ResultReport{
		Result: out.Result, Branch: out.Branch, Summary: out.Summary, Checks: out.Checks, Findings: out.Findings,
	})
	if err != nil {
		return "", fmt.Errorf("agent: report result: %w", err)
	}

	switch decision {
	case "blocked":
		return OutcomeBlocked, nil
	case "merge":
		return r.merge(ctx, task, scenario, out.Branch, false)
	case "hold":
		return r.awaitAndMaybeMerge(ctx, task, scenario, out.Branch)
	default:
		return "", fmt.Errorf("agent: unknown decision %q", decision)
	}
}

// countMergeFailure records a consecutive merge failure for taskID and reports the new count.
func (r *Runner) countMergeFailure(taskID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mergeFails[taskID]++
	return r.mergeFails[taskID]
}

// clearMergeFailures forgets a task's failure streak (it merged, or it was blocked).
func (r *Runner) clearMergeFailures(taskID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.mergeFails, taskID)
}

// merge squash-merges the verified branch and reports it merged → task done.
func (r *Runner) merge(ctx context.Context, task agentclient.TaskInfo, scenario agentclient.ScenarioInfo, branch string, approved bool) (Outcome, error) {
	res, err := r.ex.Merge(ctx, task, scenario, branch, approved)
	if err != nil {
		// Bound the retry loop: after MaxMergeAttempts consecutive failures the conflict is
		// inherent to the branch, so report it blocked (a verdict) instead of erroring out and
		// letting the lease-release bounce the task back to ready for an identical retry.
		if n := r.countMergeFailure(task.ID); n >= MaxMergeAttempts {
			r.clearMergeFailures(task.ID)
			reason := fmt.Sprintf("merge failed %d consecutive times; not retrying: %v", n, err)
			r.log.Warn("agent: merge failed repeatedly; reporting blocked", "task", task.ID, "attempts", n, "err", err)
			if _, rerr := r.gw.Result(ctx, r.cfg.ProjectID, task.ID, agentclient.ResultReport{
				Result: "blocked", Branch: branch, Summary: reason,
			}); rerr != nil {
				return "", fmt.Errorf("agent: report merge-blocked %q: %w", task.ID, rerr)
			}
			return OutcomeBlocked, nil
		}
		return "", fmt.Errorf("agent: merge %q: %w", task.ID, err)
	}
	r.clearMergeFailures(task.ID)
	// P2b: persist the full diff from the verified worktree on EVERY merge path (auto + approved-held)
	// so GET .../diff always serves a full-file native diff. The once-per-Run early emit misses the
	// held→approved→merge and re-verify paths — exactly the big multi-file tasks. Best-effort.
	if res.Patch != "" {
		_ = r.gw.StoreTaskDiff(context.WithoutCancel(ctx), r.cfg.ProjectID, task.ID, agentclient.TaskDiffBody{
			Base: res.Base, Branch: res.Branch, Patch: res.Patch, Truncated: res.Truncated,
		})
	}
	if err := r.gw.Merged(ctx, r.cfg.ProjectID, task.ID, res.SHA); err != nil {
		return "", fmt.Errorf("agent: report merged %q: %w", task.ID, err)
	}
	return OutcomeMerged, nil
}

// awaitAndMaybeMerge polls the held task's decision until the director approves (then
// re-verify-and-merge) or aborts. It returns OutcomeHeld if ctx is cancelled while
// still pending (the task stays held for the next agent run — nothing is lost).
func (r *Runner) awaitAndMaybeMerge(ctx context.Context, task agentclient.TaskInfo, scenario agentclient.ScenarioInfo, branch string) (Outcome, error) {
	for {
		state, err := r.gw.Decision(ctx, r.cfg.ProjectID, task.ID)
		if err != nil {
			return "", fmt.Errorf("agent: poll decision %q: %w", task.ID, err)
		}
		switch state {
		case "approved":
			return r.merge(ctx, task, scenario, branch, true)
		case "aborted":
			return OutcomeAborted, nil
		}
		select {
		case <-ctx.Done():
			return OutcomeHeld, nil // still pending; release frees the lease, task stays held
		case <-time.After(r.poll):
		}
	}
}

// scenarioFor fetches the task's scenario (acceptance + holdout). A missing scenario
// is not fatal — the agent develops against whatever the recipe encodes — so a fetch
// error yields an empty scenario and a warning.
func (r *Runner) scenarioFor(ctx context.Context, task agentclient.TaskInfo) agentclient.ScenarioInfo {
	if task.ScenarioID == "" {
		return agentclient.ScenarioInfo{}
	}
	scenarios, err := r.gw.Scenarios(ctx, r.cfg.ProjectID)
	if err != nil {
		r.log.Warn("agent: fetch scenarios failed", "task", task.ID, "err", err)
		return agentclient.ScenarioInfo{}
	}
	for _, s := range scenarios {
		if s.ID == task.ScenarioID {
			return s
		}
	}
	return agentclient.ScenarioInfo{}
}

// report is a best-effort observability emit (failures are logged, never fatal).
func (r *Runner) report(ctx context.Context, taskID, phase, kind string, payload map[string]any) {
	if err := r.gw.Report(ctx, r.cfg.ProjectID, taskID, phase, kind, payload); err != nil {
		r.log.Debug("agent: report failed", "task", taskID, "phase", phase, "kind", kind, "err", err)
	}
}

// runWithProgress runs ex.Run while emitting a periodic progress event (phase + elapsed seconds
// + files-changed) so the editor's "Now" view has a live pulse during the long, otherwise-silent
// develop/verify phases. The pulse is best-effort and stops the instant Run returns; if the
// executor has no ProgressProbe, it just runs Run. The heartbeat never blocks or fails the run.
func (r *Runner) runWithProgress(ctx context.Context, task agentclient.TaskInfo, scenario agentclient.ScenarioInfo) (RunOutcome, error) {
	pp, ok := r.ex.(ProgressProbe)
	if !ok {
		return r.ex.Run(ctx, task, scenario)
	}
	start := time.Now()
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(r.progress)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				phase, files, detail := pp.Progress(task.ID)
				payload := map[string]any{
					"elapsed_seconds": int(time.Since(start).Seconds()),
					"files_changed":   files,
					"step":            phase, // granular step (provisioning|developing|verifying)
				}
				if detail != "" {
					payload["detail"] = detail // rich activity line (📖/✍️/🔎/🤔) for the editor timeline
				}
				r.report(ctx, task.ID, eventPhase(phase), "progress", payload)
			}
		}
	}()
	out, err := r.ex.Run(ctx, task, scenario)
	close(done)
	return out, err
}

// Loop runs RunOnce repeatedly, heart-beating in the background and sleeping idle
// between no-work polls, until ctx is cancelled. It is the agent's main loop.
func (r *Runner) Loop(ctx context.Context, idle time.Duration) error {
	if idle <= 0 {
		idle = 10 * time.Second
	}
	go r.heartbeatLoop(ctx)
	for {
		outcome, err := r.RunOnce(ctx)
		if err != nil {
			r.log.Error("agent: run-once error", "err", err)
		} else if outcome != OutcomeNoWork {
			r.log.Info("agent: task outcome", "outcome", string(outcome))
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Sleep only when there is a reason to; otherwise loop straight to the next lease
		// (there may be more ready work). No-work idles briefly; a rate-limit backs off longer
		// so we don't churn provision+lease cycles against a wall only time clears (the task
		// already reverted to ready and is re-picked once the window resets; heartbeat continues
		// on its own goroutine).
		var pause time.Duration
		switch outcome {
		case OutcomeNoWork:
			pause = idle
		case OutcomeRateLimited:
			r.log.Warn("agent: rate-limited; backing off before next lease", "backoff", r.rateBackoff.String())
			pause = r.rateBackoff
		case OutcomeNoVerdict:
			// The task is back in the queue, so looping straight into the next lease would re-pick it
			// instantly and spin against whatever just broke. Back off — hard once a SECOND task has
			// failed the same way, because then it is the environment and only time (or an operator)
			// will fix it. A single blip only pauses briefly so a healthy fleet keeps its throughput.
			r.mu.Lock()
			distinct := len(r.noVerdictTasks)
			r.mu.Unlock()
			if distinct >= EnvSuspectTasks {
				pause = r.rateBackoff
				r.log.Warn("agent: no verdict from several tasks — treating the environment as down; backing off",
					"distinct_tasks_failing", distinct, "backoff", pause.String())
			} else {
				pause = noVerdictBackoff
			}
		}
		if pause > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(pause):
			}
		}
	}
}

// heartbeatLoop advances host liveness every 20s so the reaper does not free the
// agent's lease mid-task.
func (r *Runner) heartbeatLoop(ctx context.Context) {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := r.gw.Heartbeat(ctx, r.cfg.HostID); err != nil {
				r.log.Debug("agent: heartbeat failed", "err", err)
			}
		}
	}
}
