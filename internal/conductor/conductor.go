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

	"github.com/everva/conductor-platform/internal/engine"
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
		store:    d.Store,
		picker:   d.Picker,
		prov:     d.Provisioner,
		engine:   d.Engine,
		verifier: d.Verifier,
		merger:   d.Merger,
		recipe:   d.Recipe,
		hostID:   hostID,
	}, nil
}

// Tick runs ONE fresh-context tick for the project (ADR-0001 sıralılık):
//
//	PickReady -> acquire lease -> Workspace -> Develop -> Verify
//	  -> (independent pass) squash-merge with [task:<id>] + mark done
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

	task, err := c.picker.PickReady(ctx, projectID)
	if err != nil {
		if errors.Is(err, statestore.ErrNotFound) {
			// No ready task is a clean no-op: no lease taken, nothing to clean.
			return TickResult{Outcome: OutcomeNoOp}, nil
		}
		return TickResult{}, fmt.Errorf("conductor: tick: pick ready: %w", err)
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

	// Develop in the fresh worktree (A-3). Sentinel classification first (ADR-0014).
	verdict, devErr := c.engine.Develop(ctx, task, ws)
	if devErr != nil {
		return c.handleDevelopError(ctx, task, devErr)
	}

	// Independent verify (B-2): gates + hidden holdout. The merge gate is THIS
	// result, never the self-reported verdict (Rule#9).
	review, _, verErr := c.verifier.Verify(ctx, verdict, ws, c.recipe.Gates, task.ScenarioID)
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

	// Independent PASS: squash-merge into the base with a [task:<id>] trailer and
	// mark the task done (ADR-0004).
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

	return TickResult{
		Outcome:  OutcomeMerged,
		TaskID:   task.ID,
		Verdict:  verdict,
		Review:   review,
		MergeSHA: sha,
	}, nil
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
		// Tick defer; the task is left as-is for a human to resume.
		return TickResult{Outcome: OutcomeStopped, TaskID: task.ID},
			fmt.Errorf("conductor: tick: develop %q stopped: %w", task.ID, devErr)
	}
	// ErrMalformedVerdict / ErrNoVerdict / anything else -> block, never retry blind.
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
	return TickResult{Outcome: OutcomeRetry, TaskID: task.ID, Verdict: verdict, Review: review}, nil
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
