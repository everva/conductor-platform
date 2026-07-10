// Package registry holds the platform's task-selection, lease, and lifecycle
// business logic on top of the frozen StateStore contract (ADR-0010 task-ledger,
// ADR-0008 repo-başına-1 lease, ADR-0004 task lifecycle).
//
// The Registry composes a statestore.StateStore and never reaches around it:
// there is no second backing map and no global singleton. Observed status is
// always read back through the store each call, because status is a
// derived/reconciled field and not authoritative truth about git
// (ADR-0010 §Güncelleme).
package registry

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/everva/conductor-platform/internal/statestore"
)

// Task lifecycle statuses (ADR-0004). They are the canonical values the registry
// writes on an explicit transition; the store treats Status as an opaque string.
const (
	// StatusTodo is the initial, unscheduled state.
	StatusTodo = "todo"
	// StatusReady marks a task whose dependencies are satisfied.
	StatusReady = "ready"
	// StatusRunning marks a task currently being executed.
	StatusRunning = "running"
	// StatusDone marks a task that has landed. It is terminal.
	StatusDone = "done"
	// StatusBlocked marks a task that failed and is awaiting retry/recovery.
	StatusBlocked = "blocked"
	// StatusCancelled marks a task terminally cancelled by an operator (e.g. filed by mistake /
	// an auditor false-positive). Terminal like done, but distinct so it never reads as "landed";
	// PickReady never selects it (only todo/ready are pickable).
	StatusCancelled = "cancelled"
	// StatusAwaitingApproval parks a task whose gate PASSED but whose merge a human must
	// approve (governance held-for-review); the verified branch is preserved on the task.
	// PickReady selects it ONLY once Approved is set — never to re-develop it, but so an
	// agent can finish the merge (see mergeable-first ordering in PickReady).
	StatusAwaitingApproval = "awaiting-approval"
)

// ErrIllegalTransition is returned by Transition when the requested
// from→to status change is not part of the task lifecycle (ADR-0004). It is a
// comparable sentinel so callers can detect it with errors.Is.
var ErrIllegalTransition = errors.New("registry: illegal task transition")

// ErrDepsNotSatisfied is returned when a task cannot enter the ready state
// because one or more of its dependencies are not done (dep-gate, ADR-0004
// §Sonuç). A dependency that is blocked therefore keeps its dependents from ever
// becoming ready.
var ErrDepsNotSatisfied = errors.New("registry: dependencies not satisfied")

// legalTransitions maps each lifecycle status to the set of statuses it may
// transition to (ADR-0004). done is terminal; blocked re-enters ready on retry.
var legalTransitions = map[string][]string{
	StatusTodo:    {StatusReady},
	StatusReady:   {StatusRunning},
	StatusRunning: {StatusDone, StatusBlocked},
	StatusBlocked: {StatusReady},
	StatusDone:    {},
}

// Registry implements task selection, leasing, and lifecycle rules over a frozen
// StateStore. Construct it with NewRegistry and pass the store explicitly.
type Registry struct {
	store statestore.StateStore
	// capabilities is this host's capability set used for capability-routing
	// (ADR-0024 agent-per-host, ADR-0008, 2B-2). When non-empty, PickReady picks a
	// task ONLY if every Task.Requires entry is in this set (Requires ⊆ capabilities),
	// leaving tasks it cannot satisfy for a capable host (pull-based routing). When
	// empty/nil, routing is UNCONSTRAINED — PickReady picks regardless of Requires,
	// exactly as before 2B-2 (single-host setups that never declared capabilities are
	// unaffected). Populate it with WithCapabilities; it is normalized to a set there.
	capabilities map[string]struct{}
}

// Option configures a Registry at construction. Options are ADDITIVE: NewRegistry
// stays backward compatible (no option = today's behavior), so existing callers are
// unchanged and a host opts INTO capability-routing by passing WithCapabilities.
type Option func(*Registry)

// WithCapabilities configures the Registry with this host's capability set for
// capability-routing (ADR-0024, ADR-0008, 2B-2). It is ADDITIVE: a Registry built
// without it is UNCONSTRAINED (picks any pickable task regardless of Task.Requires,
// the pre-2B-2 behavior). A Registry built WITH a non-empty set picks a task only
// when Task.Requires ⊆ caps; a task whose Requires is not satisfied is SKIPPED and
// left for a capable host (pull-based). A task with empty Requires is pickable by
// ANY host (constrained or not).
//
// An empty/all-blank caps slice is treated as "no capabilities configured" =
// UNCONSTRAINED (the same as not passing the option at all). This "empty =
// unconstrained" choice preserves single-host setups that never declared
// capabilities; a stricter "empty = match only no-requires tasks" is a future
// opt-in, not this. Entries are trimmed; blank entries are dropped.
func WithCapabilities(caps []string) Option {
	return func(r *Registry) {
		set := make(map[string]struct{}, len(caps))
		for _, c := range caps {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			set[c] = struct{}{}
		}
		if len(set) == 0 {
			r.capabilities = nil
			return
		}
		r.capabilities = set
	}
}

// NewRegistry returns a Registry backed by the given StateStore. The store is the
// Registry's only state; the Registry adds no caching of its own. Options are
// ADDITIVE — calling NewRegistry with no options yields the pre-2B-2 behavior
// (UNCONSTRAINED capability-routing), so existing callers are unaffected.
func NewRegistry(store statestore.StateStore, opts ...Option) *Registry {
	r := &Registry{store: store}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// PickReady returns the single highest-priority pickable task for the project, or
// a wrapped statestore.ErrNotFound when none is pickable.
//
// A task is pickable when its status is todo or ready, every dependency resolves
// to a task with status done, the project holds no active lease (ADR-0008), AND
// this host satisfies the task's required capabilities (capability-routing,
// ADR-0024/ADR-0008, 2B-2). Among pickable tasks the lowest task ID wins, giving a
// deterministic, stable result regardless of the order ListTasks returns them in.
//
// Capability-routing (2B-2): when the Registry was built WithCapabilities (a
// non-empty set), a task is pickable only if Task.Requires ⊆ capabilities; a task
// whose Requires is not satisfied is SKIPPED and left for a capable host (pull-based
// — no error, the next ready task is considered). When the Registry has NO
// capabilities configured (the default), routing is UNCONSTRAINED: PickReady picks
// regardless of Task.Requires, exactly as before 2B-2. A task with empty Requires is
// pickable by ANY host (constrained or not).
func (r *Registry) PickReady(ctx context.Context, projectID string) (statestore.Task, error) {
	// Lease-gate: a held lease means the repo is busy, so nothing is pickable.
	if _, err := r.store.GetLease(ctx, projectID); err == nil {
		return statestore.Task{}, fmt.Errorf("pick ready for project %q: lease held: %w", projectID, statestore.ErrNotFound)
	} else if !errors.Is(err, statestore.ErrNotFound) {
		return statestore.Task{}, fmt.Errorf("pick ready for project %q: check lease: %w", projectID, err)
	}

	tasks, err := r.store.ListTasks(ctx, projectID)
	if err != nil {
		return statestore.Task{}, fmt.Errorf("pick ready for project %q: list tasks: %w", projectID, err)
	}

	pickable := make([]statestore.Task, 0, len(tasks))
	for _, t := range tasks {
		if !isPickable(t) {
			continue
		}
		// Capability-routing gate (2B-2): skip a task this host cannot run, leaving
		// it for a capable host (pull-based). Unconstrained when no caps configured.
		if !r.capabilitiesSatisfy(t.Requires) {
			continue
		}
		ok, derr := r.depsDone(ctx, t)
		if derr != nil {
			return statestore.Task{}, fmt.Errorf("pick ready for project %q: evaluate deps of %q: %w", projectID, t.ID, derr)
		}
		if ok {
			pickable = append(pickable, t)
		}
	}
	if len(pickable) == 0 {
		return statestore.Task{}, fmt.Errorf("pick ready for project %q: %w", projectID, statestore.ErrNotFound)
	}

	// MERGEABLE-FIRST, then REMEDIATION-FIRST ordering. An approved held task is FINISHED,
	// VERIFIED work waiting only for a squash-merge, so it lands before anything new starts.
	// Then fix-tasks (fabrication/audit/E2E-failure remediation of ALREADY-SHIPPED screens)
	// outrank new feature work — a known defect in production is worth more than the next new
	// screen. Within each rank, lowest ID wins (stable, deterministic, deps already gated
	// above). Feature tasks keep their exact prior order relative to one another.
	slices.SortFunc(pickable, func(a, b statestore.Task) int {
		if ra, rb := pickRank(a), pickRank(b); ra != rb {
			return ra - rb
		}
		return cmpString(a.ID, b.ID)
	})
	return pickable[0], nil
}

// isPickable reports whether a task may be leased.
//
// todo/ready are the ordinary develop candidates. An APPROVED awaiting-approval task with a
// preserved branch is ALSO pickable — not to re-develop it, but so an agent can perform the
// squash-merge the director authorized. Without this, approval is only ever observed by the
// ONE agent process still polling /decision for that task: if that process restarts, hits its
// usage limit, or crashes before the human approves, the approval flag is set on a task that
// no PickReady will ever hand out again — verified, gate-passing work stranded forever with
// no API able to recover it. The flag is durable state; make it actionable by any agent.
func isPickable(t statestore.Task) bool {
	switch t.Status {
	case StatusTodo, StatusReady:
		return true
	case StatusAwaitingApproval:
		return t.Approved && t.Branch != ""
	default:
		return false
	}
}

// pickRank orders pickable tasks: 0 = approved-and-waiting-to-merge, 1 = remediation, 2 = feature.
func pickRank(t statestore.Task) int {
	if t.Status == StatusAwaitingApproval {
		return 0
	}
	return 1 + remediationRank(t.ID)
}

// remediationRank returns 0 for a remediation/fix task (so it is picked before feature
// work) and 1 otherwise. Remediation tasks are filed by the quality loops with a stable
// ID prefix: A-FIX-* (director/honesty fixes), A-AUDIT-* (semantic auditor findings),
// A-E2E-* (failing real-backend E2E). The convention keeps this a pure, allocation-free
// string check with no schema change.
func remediationRank(id string) int {
	for _, p := range []string{"A-FIX-", "A-AUDIT-", "A-E2E-"} {
		if strings.HasPrefix(id, p) {
			return 0
		}
	}
	return 1
}

// AcquireLease takes the repo-scoped lease for the task via the store, enforcing
// one active lease per project: a second acquire while a lease is held returns an
// error (ADR-0008). It delegates to the store rather than tracking leases itself.
func (r *Registry) AcquireLease(ctx context.Context, l statestore.Lease) error {
	if err := r.store.AcquireLease(ctx, l); err != nil {
		return fmt.Errorf("acquire lease for project %q: %w", l.ProjectID, err)
	}
	return nil
}

// ReleaseLease releases the lease on the project via the store. It is idempotent:
// releasing a project that holds no lease is not an error.
func (r *Registry) ReleaseLease(ctx context.Context, projectID string) error {
	if err := r.store.ReleaseLease(ctx, projectID); err != nil {
		return fmt.Errorf("release lease for project %q: %w", projectID, err)
	}
	return nil
}

// ReleaseLeaseOwned releases the lease via the store ONLY when it is still held by
// the given (hostID, taskID) owner (ADR-0021 additive, C-2). It delegates to the
// store's owner-scoped release; releasing a lease the caller no longer owns (after
// a reap→re-acquire by another host) is a clean idempotent no-op, never an error.
func (r *Registry) ReleaseLeaseOwned(ctx context.Context, projectID, hostID, taskID string) error {
	if err := r.store.ReleaseLeaseOwned(ctx, projectID, hostID, taskID); err != nil {
		return fmt.Errorf("release lease (owned) for project %q: %w", projectID, err)
	}
	return nil
}

// Transition moves the task to the target status, persisting it through
// UpdateTask only when the change is legal (ADR-0004). The current status is read
// back from the store each call, never cached. An illegal transition returns
// ErrIllegalTransition and does not persist; a transition into ready additionally
// requires every dependency to be done (dep-gate) and otherwise returns
// ErrDepsNotSatisfied without persisting. A missing task wraps
// statestore.ErrNotFound.
func (r *Registry) Transition(ctx context.Context, taskID, to string) (statestore.Task, error) {
	current, err := r.store.GetTask(ctx, taskID)
	if err != nil {
		return statestore.Task{}, fmt.Errorf("transition task %q: %w", taskID, err)
	}

	if !transitionAllowed(current.Status, to) {
		return statestore.Task{}, fmt.Errorf("transition task %q from %q to %q: %w", taskID, current.Status, to, ErrIllegalTransition)
	}

	if to == StatusReady {
		ok, derr := r.depsDone(ctx, current)
		if derr != nil {
			return statestore.Task{}, fmt.Errorf("transition task %q to ready: evaluate deps: %w", taskID, derr)
		}
		if !ok {
			return statestore.Task{}, fmt.Errorf("transition task %q to ready: %w", taskID, ErrDepsNotSatisfied)
		}
	}

	current.Status = to
	if err := r.store.UpdateTask(ctx, current); err != nil {
		return statestore.Task{}, fmt.Errorf("transition task %q: persist: %w", taskID, err)
	}
	return current, nil
}

// depsDone reports whether every dependency of t resolves to a task with status
// done. A dependency that is missing, blocked, or otherwise not done yields
// false (it gates the dependent); a non-ErrNotFound store error is propagated.
func (r *Registry) depsDone(ctx context.Context, t statestore.Task) (bool, error) {
	for _, depID := range t.Deps {
		dep, err := r.store.GetTask(ctx, depID)
		if err != nil {
			if errors.Is(err, statestore.ErrNotFound) {
				return false, nil
			}
			return false, fmt.Errorf("get dependency %q: %w", depID, err)
		}
		if dep.Status != StatusDone {
			return false, nil
		}
	}
	return true, nil
}

// capabilitiesSatisfy reports whether this host may run a task with the given
// Requires under capability-routing (2B-2). It encodes the precise semantics:
//
//   - No capabilities configured on the Registry (nil/empty) → UNCONSTRAINED: any
//     task is satisfiable regardless of Requires (the pre-2B-2 behavior; preserves
//     single-host setups that never declared capabilities). Always returns true.
//   - Capabilities configured (non-empty) → the task is satisfiable only when
//     Requires ⊆ capabilities. A task with empty Requires is satisfiable by ANY host
//     (the subset of the empty set is always satisfied), so it returns true here too.
func (r *Registry) capabilitiesSatisfy(requires []string) bool {
	if len(r.capabilities) == 0 {
		return true // unconstrained host (empty = today's behavior).
	}
	// Normalize Task.Requires the SAME way WithCapabilities normalizes the host's
	// capability set (L2): trim each entry and DROP blanks. The host set is already
	// trimmed/blank-dropped at construction, so an un-normalized requirement like
	// "docker " (trailing space) or "" would otherwise never match a clean capability
	// and silently route the task nowhere. Trimming both sides makes a stray-whitespace
	// or blank Requires entry route correctly instead of becoming a silent never-match.
	for _, req := range requires {
		req = strings.TrimSpace(req)
		if req == "" {
			continue // a blank requirement constrains nothing (matches the host-side drop).
		}
		if _, ok := r.capabilities[req]; !ok {
			return false
		}
	}
	return true
}

// transitionAllowed reports whether moving from the current status to to is a
// legal lifecycle edge (ADR-0004).
func transitionAllowed(from, to string) bool {
	return slices.Contains(legalTransitions[from], to)
}

// cmpString orders strings ascending for a deterministic, stable pick.
func cmpString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
