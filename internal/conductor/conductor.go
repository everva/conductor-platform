// Package conductor is the Faz-1a integration spine (ADR-0001): one fresh-context
// tick that ORCHESTRATES the already-built, frozen-contract components — the
// registry (A-2), provisioner (B-1), the engine.EngineAdapter (A-3), the verifier
// (B-2), and the reconciler (B-3) — over the frozen statestore.StateStore.
//
// The conductor adds NO new business logic into its collaborators and changes NO
// frozen signature: it wires them. Every collaborator is consumed through a
// narrow interface, so a Conductor is constructed by injection and the tick is
// unit-testable with fakes (no global singleton).
//
// One Tick = one fresh task: PickReady -> acquire repo lease -> Workspace ->
// Develop -> Verify -> (independent pass only) squash-merge into BaseBranch with a
// `[task:<id>]` trailer (ADR-0004) -> mark done. The lease is released and the
// worktree cleaned in defers regardless of outcome. Resilience follows ADR-0014:
// ErrAuthExpired stops the tick; ErrMalformedVerdict/ErrNoVerdict block the task;
// verify changes-requested retries up to the cap then blocks (ADR-0004). The merge
// rides ONLY on the independent verify pass, never the self-reported Verdict
// (Rule#9, ADR-0003/0018).
package conductor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/events"
	"github.com/everva/conductor-platform/internal/governance"
	"github.com/everva/conductor-platform/internal/governor"
	"github.com/everva/conductor-platform/internal/statestore"
	"github.com/everva/conductor-platform/internal/verify"
)

// MaxRetries is the changes-requested retry cap before a task is blocked
// (ADR-0004 §max 2). A task whose RetryCount has reached this is blocked rather
// than retried again.
const MaxRetries = 2

// Outcome is the terminal classification of a tick, surfaced on TickResult so a
// caller (and tests) can assert what happened without re-deriving it.
type Outcome string

const (
	// OutcomeNoOp means no task was ready: a clean no-op, no lease taken.
	OutcomeNoOp Outcome = "noop"
	// OutcomeMerged means develop+verify passed and the branch was squash-merged
	// and the task marked done.
	OutcomeMerged Outcome = "merged"
	// OutcomeRetry means verify requested changes and the task was left ready for
	// another tick (under the retry cap).
	OutcomeRetry Outcome = "retry"
	// OutcomeBlocked means the task was blocked (malformed/no verdict, or the
	// retry cap was hit) and will not be retried automatically.
	OutcomeBlocked Outcome = "blocked"
	// OutcomeStopped means the tick stopped without touching the task's lifecycle
	// because the engine hit an auth wall (ADR-0014); a human must intervene.
	OutcomeStopped Outcome = "stopped"
	// OutcomeDenied means admission control (the governor) denied starting new
	// work this tick: a clean no-op that took no lease and did no develop. The
	// denial reason is logged on the TickResult (ADR-0008 concurrency bounds).
	OutcomeDenied Outcome = "denied"
	// OutcomePaused means the project is paused via the control reverse-channel
	// (ADR-0011 §4): a clean no-op that takes NO lease, runs NO develop, and does
	// NO merge. The pause intent is persisted on the project (Readiness=paused) by
	// conductorctl through the SHARED StateStore, so a separate daemon process
	// reading the same store honors it. Resume clears it and ticks proceed again.
	OutcomePaused Outcome = "paused"
	// OutcomeAborted means an operator aborted the in-flight develop via the control
	// reverse-channel (ADR-0020 follow-up / F-2): the daemon's watcher cancelled the
	// develop child context (killing the performer process group), so develop
	// returned cancelled and the tick did NO verify and NO merge. The task is
	// reverted to a SAFE, RE-RUNNABLE state (back to ready) and its abort flag is
	// cleared, so a later tick re-attempts it from a fresh context (ADR-0001).
	OutcomeAborted Outcome = "aborted"
	// OutcomeHeld means develop+verify passed but the governance policy requires a
	// HUMAN approval for the task's risk tier (ADR-0003): the green gate is
	// necessary but not sufficient, so the conductor did NOT merge. The task is
	// parked in the awaiting-approval status with its VERIFIED per-task branch
	// recorded (preserved for a later merge), and an intervention-needed event is
	// emitted. No [task:<id>] trailer lands on the base for a held task.
	OutcomeHeld Outcome = "held"
	// OutcomeApprovedMerged means an operator APPROVED a previously-held task
	// (governance N-10, Faz-1.5-b) and this tick merged the PRESERVED verified
	// branch WITHOUT re-developing: it re-attached the held task's verified branch,
	// re-ran the cheap verify gate for base-drift safety, and squash-merged on a
	// pass. The [task:<id>] trailer lands and the task is done. No develop ran.
	OutcomeApprovedMerged Outcome = "approved-merged"
	// OutcomeApprovedRejected means an operator approved a held task but the cheap
	// re-verify of the preserved branch FAILED (the base drifted so the verified
	// work no longer passes the gate): the conductor honestly did NOT merge and
	// blocked the task instead of fake-greening it. The approval is cleared so a
	// re-approval after a fix is a fresh decision.
	OutcomeApprovedRejected Outcome = "approved-rejected"
)

// TickResult is the structured result of a single tick.
type TickResult struct {
	// Outcome is the terminal classification of the tick.
	Outcome Outcome
	// TaskID is the task the tick operated on, or "" for a no-op.
	TaskID string
	// Verdict is the engine's self-reported Verdict (informational only; the merge
	// gate is the independent Review, not this).
	Verdict engine.Verdict
	// Review is the independent verifier's result, when verify ran.
	Review engine.ReviewResult
	// MergeSHA is the squash-merge commit on the base branch, set only on a merge.
	MergeSHA string
	// DenyReason is the governor's structured denial reason, set only when the
	// Outcome is OutcomeDenied (admission control bounded concurrency this tick).
	DenyReason governor.Reason
	// HoldReason is the governance policy's structured reason for holding the task
	// for a human, set only when the Outcome is OutcomeHeld (ADR-0003 risk-layered
	// merge: high-tier task passed the gate but a human must approve the merge).
	HoldReason governance.Reason
}

// Picker selects the next ready task and manages the repo-scoped lease. It is the
// registry (A-2) seam, narrowed to what the tick needs so a fake can drive it.
type Picker interface {
	PickReady(ctx context.Context, projectID string) (statestore.Task, error)
	AcquireLease(ctx context.Context, l statestore.Lease) error
	ReleaseLease(ctx context.Context, projectID string) error
}

// Workspacer cuts and tears down the per-task worktree. It is the provisioner
// (B-1) seam. Cleanup is honored in a defer regardless of tick outcome.
type Workspacer interface {
	Workspace(ctx context.Context, project statestore.Project, task statestore.Task) (engine.Workspace, error)
	Cleanup(ctx context.Context, ws engine.Workspace) error
}

// Verifier runs the independent, deterministic verify gate plus the hidden
// holdout (B-2). It is narrowed to Verify; the conductor only reads the returned
// ReviewResult.Result for the merge gate (Rule#9). Argument order matches
// verify.Verifier.Verify so the concrete type satisfies it directly.
type Verifier interface {
	Verify(ctx context.Context, verdict engine.Verdict, ws engine.Workspace, gates []verify.Gate, holdoutRef string) (engine.ReviewResult, []engine.Check, error)
}

// Merger squash-merges a verified per-task branch into the base branch with the
// `[task:<id>]` trailer (ADR-0004) and returns the resulting commit SHA. It is a
// seam so the tick is testable with a fake and Faz-1b can swap the git mechanic;
// GitMerger is the real implementation.
type Merger interface {
	SquashMerge(ctx context.Context, project statestore.Project, task statestore.Task, ws engine.Workspace) (sha string, err error)
}

// Admitter is the resource-governor admission seam (ADR-0008, N-5): the tick
// consults it BEFORE leasing/developing so global cap, repo-per-1, and host-load
// bounds gate new work. It is narrowed to Admit so a fake can drive the tick;
// governor.Governor satisfies it. The field is OPTIONAL on Deps — a nil Admitter
// means admit-all (backward compatible), so existing wiring is unaffected.
type Admitter interface {
	Admit(ctx context.Context, projectID string) (governor.Decision, error)
}

// Emitter is the OPTIONAL observability seam (ADR-0011): the tick publishes
// lifecycle events (started/decision/merge/intervention-needed) at its natural
// phase boundaries so a UI can watch live. It is narrowed to Publish so a fake
// (or events.EventBus) satisfies it. The field is OPTIONAL on Deps — a nil
// Emitter means no events are emitted (backward compatible), so existing wiring
// and tests are unaffected.
type Emitter interface {
	Publish(ctx context.Context, ev events.Event) error
}

// Policy is the OPTIONAL governance seam (ADR-0003, N-10): AFTER the independent
// verify gate PASSES, the tick consults it to decide whether the task's risk tier
// permits an automatic merge or requires a HUMAN approval first. It is narrowed to
// MergeMode so a fake can drive the tick; governance.Policy satisfies it. The field
// is OPTIONAL on Deps — a nil Policy means auto-merge-all (the pre-N-10 behavior),
// so existing wiring and tests are unaffected.
type Policy interface {
	MergeMode(task statestore.Task) governance.Decision
}

// Pauser is the OPTIONAL control-reverse-channel seam (ADR-0011 §4): BEFORE
// PickReady/lease/develop, the tick consults it to learn whether the project is
// paused by an operator (conductorctl pause). When it reports paused, the tick is
// a clean no-op (OutcomePaused) — NO lease, NO develop, NO merge. It is narrowed
// to Paused so the durable, cross-process pause check is the ONLY thing the tick
// depends on; the operator-side Pause/Resume live behind the same control seam in
// conductorctl. The field is OPTIONAL on Deps — a nil Pauser means never-paused
// (the pre-P3-3 behavior), so existing wiring and tests are unaffected.
type Pauser interface {
	Paused(ctx context.Context, projectID string) (bool, error)
}

// Aborter is the OPTIONAL control reverse-channel ABORT seam (ADR-0011 §4,
// ADR-0020 follow-up, F-2). While a task's develop is in flight, the tick runs a
// WATCHER that polls AbortRequested on the LEASED task; when an operator has set it
// (conductorctl abort, persisted on the SHARED store so this separate daemon
// process sees it), the watcher cancels the develop child context — and because the
// performer runs via exec.CommandContext with its own process group (Setpgid),
// cancelling that context kills the WHOLE performer process group. The develop then
// returns a context-cancelled error which the tick maps to a SAFE revert (back to
// ready, NO verify, NO merge). ClearAbort is called as part of handling so the
// re-run is not immediately re-aborted by the stale flag. The field is OPTIONAL on
// Deps — a nil Aborter means no abort watcher runs (the pre-F-2 behavior), so
// existing wiring and tests are unaffected.
type Aborter interface {
	// AbortRequested reports whether the task has a pending abort signal.
	AbortRequested(ctx context.Context, taskID string) (bool, error)
	// ClearAbort clears the abort signal so a re-run is not re-aborted. Idempotent.
	ClearAbort(ctx context.Context, taskID string) error
}

// Approver is the OPTIONAL governance human-hold APPROVE seam (ADR-0003, N-10,
// Faz-1.5-b). BEFORE PickReady, the tick consults it to learn whether the project
// has an APPROVED task that was previously HELD awaiting a human (a T3/T4 task whose
// gate passed). When one exists, the tick merges its PRESERVED verified branch
// WITHOUT re-developing instead of picking new work. It is narrowed to
// PendingApproval so the durable, cross-process approval check is the ONLY thing the
// tick depends on; the operator-side RequestApprove lives behind the same store-
// backed seam (StoreApprover) in conductorctl. The field is OPTIONAL on Deps — a nil
// Approver means no approve-merge step runs (the pre-Faz-1.5-b behavior), so existing
// wiring and tests are unaffected.
type Approver interface {
	// PendingApproval reports the project's approved-and-held task ready to merge,
	// if any: the task and true when one exists, the zero task and false otherwise.
	PendingApproval(ctx context.Context, projectID string) (statestore.Task, bool, error)
}

// ReAttacher is the OPTIONAL provisioner capability the approve-merge step needs
// (Faz-1.5-b): re-attach a worktree to an EXISTING preserved branch (the held task's
// verified branch) rather than cutting a fresh one from base. The conductor type-
// asserts its Workspacer for this at approve time; a provisioner that does not
// implement it makes an approval surface a clear error rather than silently re-
// cutting (which would discard the verified work). *provisioner.Provisioner
// satisfies it via WorkspaceForBranch. It is kept SEPARATE from Workspacer so the
// frozen Workspacer seam is unchanged (additive).
type ReAttacher interface {
	WorkspaceForBranch(ctx context.Context, project statestore.Project, task statestore.Task, branch string) (engine.Workspace, error)
}

// Recipe carries the per-project gate/holdout binding the tick hands to the
// verifier (ADR-0009). It is injected, not a global.
type Recipe struct {
	// Gates are the deterministic recipe gates the verifier runs over the develop
	// worktree.
	Gates []verify.Gate
}

// Conductor orchestrates one tick over its injected collaborators. Construct it
// with New; it holds no state of its own beyond the injected seams.
type Conductor struct {
	store    statestore.StateStore
	picker   Picker
	prov     Workspacer
	engine   engine.EngineAdapter
	verifier Verifier
	merger   Merger
	recipe   Recipe
	hostID   string
	governor Admitter
	emitter  Emitter
	policy   Policy
	pauser   Pauser
	aborter  Aborter
	approver Approver
	// abortPoll is how often the running-develop watcher polls the abort signal.
	// Zero means the default (defaultAbortPoll); it is overridable so tests can
	// drive the watcher deterministically without a wall-clock dependency.
	abortPoll time.Duration
}

// Deps bundles the injected collaborators so New has a single, named-field
// constructor argument rather than a long positional list.
type Deps struct {
	// Store is the frozen authoritative state (projects, tasks, leases).
	Store statestore.StateStore
	// Picker selects the ready task and manages the lease (registry, A-2).
	Picker Picker
	// Provisioner cuts/tears down the per-task worktree (B-1).
	Provisioner Workspacer
	// Engine develops the task in the worktree (A-3).
	Engine engine.EngineAdapter
	// Verifier runs the independent verify gate + holdout (B-2).
	Verifier Verifier
	// Merger squash-merges a verified branch into the base (ADR-0004).
	Merger Merger
	// Recipe binds the per-project gates/holdout for verify (ADR-0009).
	Recipe Recipe
	// HostID identifies this host on the leases it acquires.
	HostID string
	// Governor is the OPTIONAL admission controller (ADR-0008, N-5). When set,
	// the tick consults it before leasing/developing and cleanly no-ops on a
	// deny. A nil Governor means admit-all, keeping construction backward
	// compatible (no resource bounds) — callers opt in by injecting one.
	Governor Admitter
	// Emitter is the OPTIONAL observability bus (ADR-0011, N-9). When set, the
	// tick publishes lifecycle events at phase boundaries. A nil Emitter means
	// no events are emitted, keeping construction backward compatible — callers
	// opt in by injecting an events.EventBus.
	Emitter Emitter
	// Policy is the OPTIONAL governance merge policy (ADR-0003, N-10). When set,
	// the tick consults it after a green verify gate and HOLDS high-tier tasks for
	// a human instead of auto-merging. A nil Policy means auto-merge-all (the
	// pre-N-10 behavior), keeping construction backward compatible — callers opt
	// in by injecting a governance.Policy.
	Policy Policy
	// Pauser is the OPTIONAL control reverse-channel (ADR-0011 §4, P3-3). When set,
	// the tick consults it BEFORE leasing/developing and cleanly no-ops
	// (OutcomePaused) on a paused project. A nil Pauser means never-paused (the
	// pre-P3-3 behavior), keeping construction backward compatible — the daemon
	// opts in by injecting a store-backed controller that reads the SHARED store so
	// a pause set by a separate conductorctl process is honored.
	Pauser Pauser
	// Aborter is the OPTIONAL control reverse-channel ABORT seam (ADR-0011 §4, F-2).
	// When set, the tick runs a watcher during develop that polls the leased task's
	// abort signal off the SHARED store and cancels the in-flight develop (killing
	// the performer process group) when an operator sets it via conductorctl abort.
	// A nil Aborter means no watcher runs (the pre-F-2 behavior), keeping
	// construction backward compatible — the daemon opts in by injecting a
	// store-backed aborter.
	Aborter Aborter
	// Approver is the OPTIONAL governance human-hold APPROVE seam (ADR-0003, N-10,
	// Faz-1.5-b). When set, the tick consults it BEFORE PickReady and, on an approved
	// held task, MERGES its preserved verified branch without re-developing. A nil
	// Approver means no approve-merge step runs (the pre-Faz-1.5-b behavior), keeping
	// construction backward compatible — the daemon opts in by injecting a store-
	// backed approver that reads the SHARED store so an approval set by a separate
	// conductorctl process is honored.
	Approver Approver
	// AbortPoll overrides the running-develop watcher's poll interval. Zero selects
	// the default (defaultAbortPoll). It exists so a test can drive the watcher
	// deterministically (a tiny interval) without depending on wall-clock timing.
	AbortPoll time.Duration
}

// New returns a Conductor wired from the injected collaborators. It errors if any
// required seam is nil so a misconfigured tick fails fast rather than panicking
// mid-flight.
func New(d Deps) (*Conductor, error) {
	switch {
	case d.Store == nil:
		return nil, errors.New("conductor: nil store")
	case d.Picker == nil:
		return nil, errors.New("conductor: nil picker")
	case d.Provisioner == nil:
		return nil, errors.New("conductor: nil provisioner")
	case d.Engine == nil:
		return nil, errors.New("conductor: nil engine")
	case d.Verifier == nil:
		return nil, errors.New("conductor: nil verifier")
	case d.Merger == nil:
		return nil, errors.New("conductor: nil merger")
	}
	hostID := d.HostID
	if hostID == "" {
		hostID = "local"
	}
	return &Conductor{
		store:     d.Store,
		picker:    d.Picker,
		prov:      d.Provisioner,
		engine:    d.Engine,
		verifier:  d.Verifier,
		merger:    d.Merger,
		recipe:    d.Recipe,
		hostID:    hostID,
		governor:  d.Governor,
		emitter:   d.Emitter,
		policy:    d.Policy,
		pauser:    d.Pauser,
		aborter:   d.Aborter,
		approver:  d.Approver,
		abortPoll: d.AbortPoll,
	}, nil
}

// defaultAbortPoll is how often the running-develop watcher polls the abort signal
// when no AbortPoll is configured. 1s is responsive enough that an operator's
// abort cancels the performer within a couple seconds, while cheap on the store.
const defaultAbortPoll = 1 * time.Second

// emit publishes an event on the optional observability bus (ADR-0011). It is a
// no-op when no emitter is injected, so emission is non-breaking for callers
// that don't opt in. Emission errors are swallowed: observability must never
// fail or alter a tick's outcome.
func (c *Conductor) emit(ctx context.Context, task statestore.Task, phase events.Phase, kind events.Kind, payload map[string]any) {
	if c.emitter == nil {
		return
	}
	_ = c.emitter.Publish(ctx, events.Event{
		Project: task.ProjectID,
		Task:    task.ID,
		Phase:   phase,
		Kind:    kind,
		Payload: payload,
	})
}

// Tick runs ONE fresh-context tick for the project (ADR-0001 sıralılık):
//
//	PickReady -> admit (governor) -> acquire lease -> Workspace -> Develop -> Verify
//	  -> (independent pass, auto-merge tier) squash-merge with [task:<id>] + mark done
//	  -> (independent pass, human-required tier) HOLD: no merge, mark blocked + emit
//	     intervention-needed (ADR-0003 risk-layered; only when a policy is injected)
//	  -> (changes-requested) retry up to MaxRetries, then block
//	  -> (malformed/no verdict) block, never fake-green
//	  -> (auth expired) STOP the tick
//
// The lease is released and the worktree cleaned in defers regardless of outcome.
// A tick with no ready task is a clean no-op (statestore.ErrNotFound handled, not
// fatal) and takes no lease. The tick carries no state between runs beyond the
// StateStore + git, so re-running re-derives from the store (idempotent with B-3).
func (c *Conductor) Tick(ctx context.Context, projectID string) (TickResult, error) {
	project, err := c.store.GetProject(ctx, projectID)
	if err != nil {
		return TickResult{}, fmt.Errorf("conductor: tick: get project %q: %w", projectID, err)
	}

	// Control reverse-channel pause-gate (ADR-0011 §4): conductorctl persists a
	// pause through the SHARED StateStore via the control seam, so this SEPARATE
	// daemon process sees it. When a Pauser is injected and reports the project
	// paused, the tick is a clean no-op — taken BEFORE PickReady/lease/develop/
	// merge — so nothing is leased and no work runs. A nil Pauser means
	// never-paused (the pre-P3-3 behavior), so existing wiring and tests proceed
	// exactly as before (backward compatible).
	if c.pauser != nil {
		paused, perr := c.pauser.Paused(ctx, projectID)
		if perr != nil {
			return TickResult{}, fmt.Errorf("conductor: tick: pause check for %q: %w", projectID, perr)
		}
		if paused {
			return TickResult{Outcome: OutcomePaused}, nil
		}
	}

	// Governance human-hold APPROVE step (ADR-0003, N-10, Faz-1.5-b): BEFORE picking
	// new work, check whether an operator APPROVED a previously-HELD task. Such a
	// task's develop+verify already PASSED and its verified branch was PRESERVED; the
	// approval merges THAT work WITHOUT re-developing. It is taken before PickReady so
	// the approved merge lands before any new develop, and because the held task is
	// parked in awaiting-approval (not ready) PickReady would never surface it anyway.
	// A nil Approver skips this entirely (pre-Faz-1.5-b behavior). It acquires the
	// repo lease (the merge mutates the clone) and releases it in a defer, mirroring
	// the normal task path.
	if c.approver != nil {
		held, ok, aerr := c.approver.PendingApproval(ctx, projectID)
		if aerr != nil {
			return TickResult{}, fmt.Errorf("conductor: tick: pending-approval check for %q: %w", projectID, aerr)
		}
		if ok {
			lease := statestore.Lease{ProjectID: projectID, HostID: c.hostID, TaskID: held.ID}
			if err := c.picker.AcquireLease(ctx, lease); err != nil {
				return TickResult{}, fmt.Errorf("conductor: tick: acquire lease for approve-merge %q: %w", projectID, err)
			}
			defer func() { _ = c.picker.ReleaseLease(context.WithoutCancel(ctx), projectID) }()
			return c.mergeApproved(ctx, project, held)
		}
	}

	task, err := c.picker.PickReady(ctx, projectID)
	if err != nil {
		if errors.Is(err, statestore.ErrNotFound) {
			// No ready task is a clean no-op: no lease taken, nothing to clean.
			return TickResult{Outcome: OutcomeNoOp}, nil
		}
		return TickResult{}, fmt.Errorf("conductor: tick: pick ready: %w", err)
	}

	// Admission control (ADR-0008, N-5): consult the governor BEFORE taking a
	// lease so global cap / repo-per-1 / host-load bounds gate new work. A deny is
	// a clean no-op — no lease, no develop, no error, no fake work — surfaced as
	// OutcomeDenied with the structured reason for the daemon to log. A nil
	// governor means admit-all (backward compatible).
	if c.governor != nil {
		dec, derr := c.governor.Admit(ctx, projectID)
		if derr != nil {
			return TickResult{}, fmt.Errorf("conductor: tick: admission for %q: %w", projectID, derr)
		}
		if !dec.Admit {
			return TickResult{Outcome: OutcomeDenied, TaskID: task.ID, DenyReason: dec.Reason}, nil
		}
	}

	// Lease the repo BEFORE develop and release it in a defer so it is reclaimed
	// on every path (success, error, panic).
	lease := statestore.Lease{ProjectID: projectID, HostID: c.hostID, TaskID: task.ID}
	if err := c.picker.AcquireLease(ctx, lease); err != nil {
		return TickResult{}, fmt.Errorf("conductor: tick: acquire lease for %q: %w", projectID, err)
	}
	defer func() {
		// Best-effort release; ReleaseLease is idempotent.
		_ = c.picker.ReleaseLease(context.WithoutCancel(ctx), projectID)
	}()

	return c.runTask(ctx, project, task)
}

// runTask provisions a fresh worktree for the task and drives develop -> verify ->
// merge, with worktree cleanup defer-guaranteed. It is split out so the lease
// defer in Tick wraps the whole task body uniformly.
func (c *Conductor) runTask(ctx context.Context, project statestore.Project, task statestore.Task) (res TickResult, err error) {
	ws, err := c.prov.Workspace(ctx, project, task)
	if err != nil {
		return TickResult{}, fmt.Errorf("conductor: tick: workspace for %q: %w", task.ID, err)
	}
	defer func() {
		// Worktree cleanup is defer-guaranteed regardless of outcome. The
		// provisioner honors its own RetainBlocked retention; the conductor always
		// asks for cleanup. WithoutCancel so a cancelled tick still reclaims disk.
		_ = c.prov.Cleanup(context.WithoutCancel(ctx), ws)
	}()

	// Develop in the fresh worktree (A-3), under the control reverse-channel ABORT
	// watcher (ADR-0020 follow-up / F-2). developWithAbort wraps the develop call in
	// a cancellable child context and, when an Aborter is injected, runs a watcher
	// goroutine that polls the leased task's abort signal off the SHARED store; when
	// an operator sets it (conductorctl abort), the child ctx is cancelled, which
	// (via exec.CommandContext + Setpgid) kills the performer process group. The
	// returned aborted flag distinguishes an operator-cancelled develop from any
	// other develop error so the tick reverts to a SAFE, re-runnable state instead
	// of blocking. With a nil Aborter this is a plain develop (pre-F-2 behavior).
	c.emit(ctx, task, events.PhaseDevelop, events.KindStarted, nil)
	verdict, aborted, devErr := c.developWithAbort(ctx, task, ws)
	if aborted {
		return c.handleAbort(ctx, task)
	}
	if devErr != nil {
		return c.handleDevelopError(ctx, task, devErr)
	}

	// Independent verify (B-2): gates + hidden holdout. The merge gate is THIS
	// result, never the self-reported verdict (Rule#9). The verifier receives the
	// scenario's REAL repo-external holdout LOCATOR (Scenario.HoldoutRef), not the
	// bare scenario id, so the injected HoldoutStore can fetch it (ADR-0018). A task
	// with no scenario / no HoldoutRef yields an empty locator, which the store
	// treats as "no holdout" (skipped cleanly), so non-holdout projects work.
	holdoutRef := c.resolveHoldoutRef(ctx, task)
	c.emit(ctx, task, events.PhaseVerify, events.KindStarted, nil)
	review, _, verErr := c.verifier.Verify(ctx, verdict, ws, c.recipe.Gates, holdoutRef)
	if verErr != nil {
		// Verify could not produce a result: block rather than fake-green.
		if blockErr := c.markBlocked(ctx, task); blockErr != nil {
			return TickResult{}, fmt.Errorf("conductor: tick: verify failed and block failed: %w", errors.Join(verErr, blockErr))
		}
		return TickResult{Outcome: OutcomeBlocked, TaskID: task.ID, Verdict: verdict},
			fmt.Errorf("conductor: tick: verify %q: %w", task.ID, verErr)
	}

	if review.Result != reviewPass {
		return c.handleChangesRequested(ctx, task, verdict, review)
	}

	// Risk-layered merge policy (ADR-0003, N-10): a green gate is necessary but not
	// always sufficient. When a policy is injected and the task's tier requires a
	// HUMAN, HOLD the task (no merge, no [task:<id>] trailer) instead of
	// auto-merging. A nil policy means auto-merge-all (pre-N-10 behavior).
	if c.policy != nil {
		if dec := c.policy.MergeMode(task); dec.HumanRequired() {
			return c.handleHumanRequired(ctx, task, ws, verdict, review, dec)
		}
	}

	// Independent PASS (and auto-merge tier): squash-merge into the base with a
	// [task:<id>] trailer and mark the task done (ADR-0004).
	sha, mErr := c.merger.SquashMerge(ctx, project, task, ws)
	if mErr != nil {
		// Merge failed AFTER an independent pass: block for inspection, never lie.
		if blockErr := c.markBlocked(ctx, task); blockErr != nil {
			return TickResult{}, fmt.Errorf("conductor: tick: merge failed and block failed: %w", errors.Join(mErr, blockErr))
		}
		return TickResult{Outcome: OutcomeBlocked, TaskID: task.ID, Verdict: verdict, Review: review},
			fmt.Errorf("conductor: tick: squash-merge %q: %w", task.ID, mErr)
	}

	if err := c.markDone(ctx, task); err != nil {
		return TickResult{}, fmt.Errorf("conductor: tick: mark done %q: %w", task.ID, err)
	}

	c.emit(ctx, task, events.PhaseMerge, events.KindMerge, map[string]any{"merge_sha": sha})
	return TickResult{
		Outcome:  OutcomeMerged,
		TaskID:   task.ID,
		Verdict:  verdict,
		Review:   review,
		MergeSHA: sha,
	}, nil
}

// resolveHoldoutRef returns the REPO-EXTERNAL hidden-holdout locator the verifier
// must fetch for this task (ADR-0018): the task's Scenario.HoldoutRef, NOT the
// bare scenario id. Before this, the tick passed task.ScenarioID straight to
// Verify, so an injected HoldoutStore received the id instead of the locator and
// could never resolve the real holdout (the gap A.1 closes).
//
// It is defensive so the merge gate never spuriously errors on holdout plumbing:
//   - an empty ScenarioID (a task with no scenario) -> "" (skip cleanly),
//   - a scenario that is not in the store (ErrNotFound) -> "" (skip cleanly),
//   - any other store error -> "" as well, since the holdout-fetch outcome is the
//     authoritative gate (a store that cannot resolve the locator will itself fail
//     the holdout, not be masked here). Returning "" lets a non-holdout project
//     verify on its public gates alone.
func (c *Conductor) resolveHoldoutRef(ctx context.Context, task statestore.Task) string {
	if task.ScenarioID == "" {
		return ""
	}
	scn, err := c.store.GetScenario(ctx, task.ScenarioID)
	if err != nil {
		return ""
	}
	return scn.HoldoutRef
}

// developWithAbort runs the engine's Develop under the control reverse-channel
// ABORT watcher (ADR-0020 follow-up / F-2). It returns the develop verdict + error
// and a third boolean reporting whether the develop was cancelled by an operator
// abort (as opposed to any other develop failure), so the caller can route an
// abort to a SAFE revert rather than a block.
//
// Mechanism: develop runs under a CHILD context derived from ctx. When an Aborter
// is injected, a watcher goroutine polls the leased task's abort signal every
// abortPoll; the FIRST time it reads true it cancels the child context. The
// performer subprocess runs via exec.CommandContext with its own process group
// (Setpgid, process_unix.go), so cancelling the child context kills the WHOLE
// performer process group, not just the leader. The watcher is always joined
// before this returns (no leaked goroutine, no post-return store access), so the
// `aborted` flag is read race-free.
//
// With a nil Aborter, no watcher runs and this is a plain develop under ctx
// (the pre-F-2 behavior), so existing wiring and tests are unaffected.
func (c *Conductor) developWithAbort(ctx context.Context, task statestore.Task, ws engine.Workspace) (engine.Verdict, bool, error) {
	if c.aborter == nil {
		v, err := c.engine.Develop(ctx, task, ws)
		return v, false, err
	}

	devCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	poll := c.abortPoll
	if poll <= 0 {
		poll = defaultAbortPoll
	}

	// aborted is written ONLY by the watcher goroutine and read ONLY after the
	// watcher has exited (the <-watcherDone join below), so no mutex is needed: the
	// channel close establishes the happens-before edge.
	var aborted bool
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		ticker := time.NewTicker(poll)
		defer ticker.Stop()
		for {
			select {
			case <-devCtx.Done():
				// Develop finished (or was cancelled by us already): stop watching.
				return
			case <-ticker.C:
				req, err := c.aborter.AbortRequested(devCtx, task.ID)
				if err != nil {
					// A transient read error must NOT cancel a healthy develop; keep
					// polling. A persistent error simply means abort never fires.
					continue
				}
				if req {
					aborted = true
					cancel() // kills the performer process group via ctx.
					return
				}
			}
		}
	}()

	v, err := c.engine.Develop(devCtx, task, ws)
	cancel()      // ensure the watcher's devCtx.Done() fires so it exits promptly.
	<-watcherDone // join: establishes happens-before for the `aborted` read.
	return v, aborted, err
}

// handleAbort reverts a task whose in-flight develop was cancelled by an operator
// abort (ADR-0020 follow-up / F-2) to a SAFE, RE-RUNNABLE state: it runs NO verify
// and NO merge (so the base branch gets NO [task:<id>] trailer for this run),
// resets the task to ready, and CLEARS the abort flag so the next tick does not
// immediately re-abort the re-run.
//
// Abort is chosen to leave the task RE-RUNNABLE (ready) rather than terminal
// (blocked): an operator abort means "stop THIS run" (a hung/wrong-path performer),
// not "this task is broken" — the develop never produced a verdict, so there is no
// failure to block on. A human who wants the task to stop permanently can block it
// out-of-band; the common case (cancel a stuck run, let the next fresh-context tick
// retry, ADR-0001) is served by ready. The lease is released by the Tick defer.
func (c *Conductor) handleAbort(ctx context.Context, task statestore.Task) (TickResult, error) {
	// Clear the durable abort signal first so a re-run is not re-aborted. Use a
	// cancel-free context: the tick's ctx may itself be cancelled (e.g. shutdown),
	// but the revert bookkeeping must still land.
	cleanCtx := context.WithoutCancel(ctx)
	if c.aborter != nil {
		if err := c.aborter.ClearAbort(cleanCtx, task.ID); err != nil {
			return TickResult{}, fmt.Errorf("conductor: tick: clear abort %q: %w", task.ID, err)
		}
	}
	c.emit(cleanCtx, task, events.PhaseDevelop, events.KindInterventionNeeded,
		map[string]any{"reason": "aborted by operator"})
	// Revert to ready (re-runnable). setStatus re-reads, so it persists over the
	// now-cleared abort flag.
	if err := c.setStatus(cleanCtx, task.ID, statusReady); err != nil {
		return TickResult{}, fmt.Errorf("conductor: tick: revert aborted task %q: %w", task.ID, err)
	}
	return TickResult{Outcome: OutcomeAborted, TaskID: task.ID}, nil
}

// reviewPass is the frozen ReviewResult.Result value the merge gate keys on
// (ADR-0003). It mirrors verify's internal constant without reaching into it.
const reviewPass = "pass"

// handleDevelopError maps a develop error onto the resilience outcomes
// (ADR-0014): an auth wall STOPS the tick without touching the task lifecycle so a
// human re-auths; a malformed/no/empty verdict (or any other develop error)
// blocks the task with no merge and no fake-green.
func (c *Conductor) handleDevelopError(ctx context.Context, task statestore.Task, devErr error) (TickResult, error) {
	if errors.Is(devErr, engine.ErrAuthExpired) {
		// Stop: retrying would just hit the wall again. Lease is released by the
		// Tick defer; the task is left as-is for a human to resume. An auth wall
		// is the canonical human-gate: signal intervention-needed (ADR-0011).
		c.emit(ctx, task, events.PhaseDevelop, events.KindInterventionNeeded,
			map[string]any{"reason": "auth expired", "error": devErr.Error()})
		return TickResult{Outcome: OutcomeStopped, TaskID: task.ID},
			fmt.Errorf("conductor: tick: develop %q stopped: %w", task.ID, devErr)
	}
	// ErrMalformedVerdict / ErrNoVerdict / anything else -> block, never retry blind.
	c.emit(ctx, task, events.PhaseDevelop, events.KindInterventionNeeded,
		map[string]any{"reason": "develop blocked", "error": devErr.Error()})
	if blockErr := c.markBlocked(ctx, task); blockErr != nil {
		return TickResult{}, fmt.Errorf("conductor: tick: develop failed and block failed: %w", errors.Join(devErr, blockErr))
	}
	return TickResult{Outcome: OutcomeBlocked, TaskID: task.ID},
		fmt.Errorf("conductor: tick: develop %q: %w", task.ID, devErr)
}

// handleChangesRequested implements the ADR-0004 retry cap: a changes-requested
// review leaves the task ready for another tick until RetryCount reaches
// MaxRetries, after which it is blocked. The merge never happens here (Rule#9
// negative: a holdout-breaking task is changes-requested and so never merges).
func (c *Conductor) handleChangesRequested(ctx context.Context, task statestore.Task, verdict engine.Verdict, review engine.ReviewResult) (TickResult, error) {
	if task.RetryCount >= MaxRetries {
		// Retry cap hit: a human must intervene (ADR-0011 human-gate).
		c.emit(ctx, task, events.PhaseReview, events.KindInterventionNeeded,
			map[string]any{"reason": "retry cap reached", "retry_count": task.RetryCount})
		if err := c.markBlocked(ctx, task); err != nil {
			return TickResult{}, fmt.Errorf("conductor: tick: block after retry cap %q: %w", task.ID, err)
		}
		return TickResult{Outcome: OutcomeBlocked, TaskID: task.ID, Verdict: verdict, Review: review}, nil
	}
	// Bump the retry counter and leave the task ready for the next tick. The next
	// PickReady re-selects it; fresh context per tick (ADR-0001).
	if err := c.bumpRetry(ctx, task); err != nil {
		return TickResult{}, fmt.Errorf("conductor: tick: bump retry %q: %w", task.ID, err)
	}
	c.emit(ctx, task, events.PhaseReview, events.KindDecision,
		map[string]any{"result": review.Result, "retry_count": task.RetryCount + 1})
	return TickResult{Outcome: OutcomeRetry, TaskID: task.ID, Verdict: verdict, Review: review}, nil
}

// handleHumanRequired implements the ADR-0003 risk-layered hold: the independent
// gate PASSED, but the task's risk tier (T3/T4 by the default policy) requires a
// HUMAN approval before the merge may land. The conductor therefore does NOT merge
// — no SquashMerge call, so the base branch gets NO [task:<id>] trailer for this
// task — and instead HOLDS the task awaiting a human, PRESERVING the verified work so
// an approval can later merge it WITHOUT re-developing (Faz-1.5-b).
//
// What "preserve" means concretely (the crux of the approve flow):
//   - the task is parked in the DISTINCT, durable StatusAwaitingApproval status
//     (NOT blocked): unambiguously an awaiting-human state, and one PickReady never
//     re-develops (it picks only todo/ready);
//   - the VERIFIED per-task branch (ws.Branch) is recorded on Task.Branch so the
//     later approve-merge knows exactly which branch to re-attach;
//   - the branch ref itself survives: the worktree Cleanup defer removes only the
//     worktree, never the branch ref in the clone, so the verified commit persists
//     until the approve-merge re-attaches it.
//
// The awaiting-human cause is also carried by OutcomeHeld + HoldReason on the
// TickResult and by the emitted intervention-needed event (ADR-0011 human-gate), so a
// UI surfaces "onay bekliyor" rather than a generic failure.
func (c *Conductor) handleHumanRequired(ctx context.Context, task statestore.Task, ws engine.Workspace, verdict engine.Verdict, review engine.ReviewResult, dec governance.Decision) (TickResult, error) {
	c.emit(ctx, task, events.PhaseReview, events.KindInterventionNeeded,
		map[string]any{"reason": "human approval required", "tier": dec.Tier, "policy_reason": string(dec.Reason), "verified_branch": ws.Branch})
	if err := c.holdForApproval(ctx, task, ws.Branch); err != nil {
		return TickResult{}, fmt.Errorf("conductor: tick: hold for human %q: %w", task.ID, err)
	}
	return TickResult{
		Outcome:    OutcomeHeld,
		TaskID:     task.ID,
		Verdict:    verdict,
		Review:     review,
		HoldReason: dec.Reason,
	}, nil
}

// holdForApproval parks the verified task in StatusAwaitingApproval and records the
// VERIFIED per-task branch on Task.Branch so the later approve-merge can re-attach
// exactly that branch (no re-develop). It re-reads to avoid clobbering concurrent
// store updates and leaves every other field intact. Approved is reset to false so a
// re-held task (re-develop then re-hold after a prior cleared approval) does not
// auto-merge on a stale approval.
func (c *Conductor) holdForApproval(ctx context.Context, task statestore.Task, branch string) error {
	cur, err := c.store.GetTask(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("get task %q: %w", task.ID, err)
	}
	cur.Status = StatusAwaitingApproval
	cur.Branch = branch
	cur.Approved = false
	if err := c.store.UpdateTask(ctx, cur); err != nil {
		return fmt.Errorf("update task %q: %w", task.ID, err)
	}
	return nil
}

// mergeApproved lands an operator-APPROVED held task by merging its PRESERVED
// verified branch WITHOUT re-developing (Faz-1.5-b, the approve flow's whole point).
//
// Chosen semantics: RE-VERIFY-THEN-MERGE (recommended over merge-directly). The
// verified branch may have been held for an arbitrarily long time, during which the
// base could have drifted (other tasks merged), so the conductor re-attaches the
// preserved branch and re-runs the CHEAP deterministic verify gate over it before
// merging. This guards Rule#9 (never fake-green): if the base drifted enough to break
// the gate, the merge is honestly refused and the task blocked, rather than merging
// stale work that no longer passes. Re-verify is cheap relative to re-develop and,
// critically, it does NOT re-roll the LLM — the human approves the work they saw, not
// a fresh roll. Develop NEVER runs here.
//
// Re-attach uses the ReAttacher capability (WorkspaceForBranch); a provisioner
// lacking it makes the approval a clear error rather than a silent re-cut-from-base
// (which would discard the verified work). The worktree is cleaned in a defer; the
// branch ref persists in the clone regardless, so a blocked re-verify leaves the work
// recoverable.
func (c *Conductor) mergeApproved(ctx context.Context, project statestore.Project, task statestore.Task) (res TickResult, err error) {
	reattacher, ok := c.prov.(ReAttacher)
	if !ok {
		return TickResult{}, fmt.Errorf("conductor: tick: approve-merge %q: provisioner cannot re-attach a preserved branch", task.ID)
	}
	if task.Branch == "" {
		return TickResult{}, fmt.Errorf("conductor: tick: approve-merge %q: no preserved verified branch recorded", task.ID)
	}

	ws, err := reattacher.WorkspaceForBranch(ctx, project, task, task.Branch)
	if err != nil {
		return TickResult{}, fmt.Errorf("conductor: tick: approve-merge re-attach %q: %w", task.ID, err)
	}
	defer func() {
		_ = c.prov.Cleanup(context.WithoutCancel(ctx), ws)
	}()

	c.emit(ctx, task, events.PhaseReview, events.KindDecision,
		map[string]any{"result": "approved", "verified_branch": task.Branch})

	// Cheap re-verify for base drift (re-verify-then-merge): NO develop. The merge
	// rides on THIS independent result, never a self-report (Rule#9). The held
	// task's verdict was already green; we pass a minimal verdict carrying the branch.
	holdoutRef := c.resolveHoldoutRef(ctx, task)
	c.emit(ctx, task, events.PhaseVerify, events.KindStarted, map[string]any{"reason": "re-verify approved work for base drift"})
	review, _, verErr := c.verifier.Verify(ctx, engine.Verdict{Branch: ws.Branch}, ws, c.recipe.Gates, holdoutRef)
	if verErr != nil || review.Result != reviewPass {
		// Base drifted (or verify could not run): honestly refuse the merge and block.
		// Clear Approved so a re-approval after a fix is a fresh decision, not a stale
		// auto-merge.
		c.emit(ctx, task, events.PhaseReview, events.KindInterventionNeeded,
			map[string]any{"reason": "approved work failed re-verify (base drift); not merged", "review": review.Result})
		if blockErr := c.blockApprovedReject(ctx, task); blockErr != nil {
			return TickResult{}, fmt.Errorf("conductor: tick: approve-merge re-verify failed and block failed: %w", errors.Join(verErr, blockErr))
		}
		return TickResult{Outcome: OutcomeApprovedRejected, TaskID: task.ID, Review: review}, nil
	}

	// Re-verify passed: squash-merge the PRESERVED verified branch with the
	// [task:<id>] trailer and mark done. NO develop ran.
	sha, mErr := c.merger.SquashMerge(ctx, project, task, ws)
	if mErr != nil {
		if blockErr := c.blockApprovedReject(ctx, task); blockErr != nil {
			return TickResult{}, fmt.Errorf("conductor: tick: approve-merge failed and block failed: %w", errors.Join(mErr, blockErr))
		}
		return TickResult{Outcome: OutcomeBlocked, TaskID: task.ID, Review: review},
			fmt.Errorf("conductor: tick: approve squash-merge %q: %w", task.ID, mErr)
	}
	if err := c.markDoneClearApproval(ctx, task); err != nil {
		return TickResult{}, fmt.Errorf("conductor: tick: approve-merge mark done %q: %w", task.ID, err)
	}
	c.emit(ctx, task, events.PhaseMerge, events.KindMerge, map[string]any{"merge_sha": sha, "approved": true})
	return TickResult{Outcome: OutcomeApprovedMerged, TaskID: task.ID, Review: review, MergeSHA: sha}, nil
}

// blockApprovedReject blocks a task whose approved work failed re-verify/merge and
// clears its Approved flag so a re-approval after a fix is a fresh decision rather
// than an immediate stale auto-merge. It re-reads to avoid clobbering concurrent
// updates.
func (c *Conductor) blockApprovedReject(ctx context.Context, task statestore.Task) error {
	cur, err := c.store.GetTask(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("get task %q: %w", task.ID, err)
	}
	cur.Status = statusBlocked
	cur.Approved = false
	if err := c.store.UpdateTask(ctx, cur); err != nil {
		return fmt.Errorf("update task %q: %w", task.ID, err)
	}
	return nil
}

// markDoneClearApproval marks the merged-on-approval task done and clears its
// Approved flag, so the terminal record carries no dangling approval signal. It
// re-reads to persist over the current store record.
func (c *Conductor) markDoneClearApproval(ctx context.Context, task statestore.Task) error {
	cur, err := c.store.GetTask(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("get task %q: %w", task.ID, err)
	}
	cur.Status = statusDone
	cur.Approved = false
	if err := c.store.UpdateTask(ctx, cur); err != nil {
		return fmt.Errorf("update task %q: %w", task.ID, err)
	}
	return nil
}

// markDone re-reads the task and sets it done (ADR-0004 terminal state). It
// re-reads so it persists the current store record, not a stale snapshot.
func (c *Conductor) markDone(ctx context.Context, task statestore.Task) error {
	return c.setStatus(ctx, task.ID, statusDone)
}

// markBlocked re-reads the task and sets it blocked (ADR-0004). No fake-green: a
// failed develop/verify/merge always lands here, never done.
func (c *Conductor) markBlocked(ctx context.Context, task statestore.Task) error {
	return c.setStatus(ctx, task.ID, statusBlocked)
}

// bumpRetry increments the task's RetryCount and leaves its status ready for the
// next tick (the task stays pickable). It re-reads to avoid clobbering concurrent
// store updates.
func (c *Conductor) bumpRetry(ctx context.Context, task statestore.Task) error {
	cur, err := c.store.GetTask(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("get task %q: %w", task.ID, err)
	}
	cur.RetryCount++
	cur.Status = statusReady
	if err := c.store.UpdateTask(ctx, cur); err != nil {
		return fmt.Errorf("update task %q: %w", task.ID, err)
	}
	return nil
}

// setStatus re-reads the task and persists the new status, leaving every other
// field intact. A missing task wraps statestore.ErrNotFound.
func (c *Conductor) setStatus(ctx context.Context, taskID, status string) error {
	cur, err := c.store.GetTask(ctx, taskID)
	if err != nil {
		return fmt.Errorf("get task %q: %w", taskID, err)
	}
	cur.Status = status
	if err := c.store.UpdateTask(ctx, cur); err != nil {
		return fmt.Errorf("update task %q: %w", taskID, err)
	}
	return nil
}

// Task lifecycle status values the conductor writes (ADR-0004). They mirror the
// registry's canonical strings without importing the registry, keeping the tick
// decoupled from the selection package's internals.
const (
	statusReady   = "ready"
	statusDone    = "done"
	statusBlocked = "blocked"
)
