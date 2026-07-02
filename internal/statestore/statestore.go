// Package statestore defines the frozen persistence contract for the conductor
// platform's registry and task ledger (ADR-0010, ADR-0013).
//
// The StateStore interface is the single seam between the platform skeleton
// (conductor loop, governor, sentinel) and whatever backing store is in use:
// an in-memory/file store in Faz-1a and central Postgres in Faz-1b. Only the
// authoritative intent — projects, tasks, leases, scenarios — lives here;
// observed runtime truth (whether a branch merged) is derived from git/gh each
// tick and never cached (ADR-0010 §3).
//
// This contract is FROZEN: later tasks fill the implementation behind it but
// must not change the signatures.
package statestore

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by lookup methods when no record matches the given
// identifier. Callers detect it with errors.Is; implementations should wrap it
// with %w to add context.
var ErrNotFound = errors.New("statestore: record not found")

// Project is a registered repository the platform operates on (ADR-0010). It
// holds intent and pointers only; live git state is derived, not stored.
type Project struct {
	// ID is the stable platform identifier for the project.
	ID string
	// Repo is the git remote (e.g. owner/name or a URL).
	Repo string
	// BaseBranch is the integration branch tasks branch from (e.g. develop).
	BaseBranch string
	// HostID is the host currently associated with the project, if any.
	HostID string
	// Readiness reports whether the project is bootstrapped enough to run tasks.
	Readiness string
	// RecipePointer locates the project's .conductor recipe (ADR-0009).
	RecipePointer string
	// GovernancePolicy names the tier→merge-mode policy applied (ADR-0003).
	GovernancePolicy string
	// Paused is the first-class run-state of the project's conductor loop
	// (ADR-0021): true means ticks are a clean no-op (OutcomePaused) until
	// resumed. It defaults to false (running) on CreateProject and is the
	// observable replacement for the ADR-0020 pause marker-task. This is an
	// ADDITIVE field on the otherwise-frozen contract.
	Paused bool
}

// Task is a unit of work scheduled against a project (ADR-0010). Status is a
// derived/reconciled field and is not authoritative (ADR-0010 §Güncelleme).
type Task struct {
	// ID is the stable identifier for the task (e.g. PRE-0).
	ID string
	// ProjectID references the owning Project.
	ProjectID string
	// Lane is the capability lane the task runs in (ADR-0008).
	Lane string
	// Tier is the risk tier (T1..T4) gating merge mode (ADR-0003).
	Tier string
	// Status is the derived lifecycle state (ready, running, blocked, done).
	Status string
	// Requires lists capabilities the task needs from a lane/host.
	Requires []string
	// Deps lists task IDs that must land before this task is ready.
	Deps []string
	// Branch is the short-lived per-task branch (ADR-0004).
	Branch string
	// ScenarioID references the Scenario describing acceptance for this task.
	ScenarioID string
	// RetryCount tracks how many times the task has been retried (ADR-0004).
	RetryCount int
	// AbortRequested is the durable control reverse-channel ABORT signal (ADR-0020
	// follow-up / F-2): true means an operator asked the conductor to CANCEL the
	// in-flight develop for this task and revert it to a safe state — NO verify, NO
	// merge. The conductor's running-develop watcher polls it on the leased task and
	// cancels the develop child context when it flips, killing the performer process
	// group; the conductor clears it as part of handling the abort so the re-run is
	// not immediately re-aborted. It defaults to false and is an ADDITIVE field on
	// the otherwise-frozen contract (ADR-0021: additive growth, no signature break).
	AbortRequested bool
	// Approved is the durable operator APPROVAL signal for a task HELD awaiting a
	// human after a green gate (governance N-10, T3/T4; Faz-1.5-b). When the
	// governance policy holds a verified task, the conductor parks it in the
	// awaiting-approval status with its verified per-task Branch recorded; an
	// operator then sets Approved=true via `conductorctl approve` (persisted on the
	// SHARED store). A later tick sees the approved+held task, RE-ATTACHES the
	// preserved verified branch (no re-develop), OPTIONALLY re-verifies for base
	// drift, and squash-merges it. The conductor clears it as the task lands. It
	// defaults to false and is an ADDITIVE field on the otherwise-frozen contract
	// (ADR-0021: additive growth, no signature break).
	Approved bool
	// LastError is the human-readable reason the task last went non-pass — the agent's
	// report Summary (e.g. "agent run failed: …malformed verdict…", "gate unresolved after
	// N rounds — needs user", "holdout: no holdout command configured"). The director's board
	// surfaces it so a stalled job explains itself instead of showing a bare "blocked". The
	// gateway sets it when it records a non-pass result and CLEARS it when the task starts a
	// fresh attempt (claim→running) or is retried. Additive on the otherwise-frozen contract
	// (ADR-0021); defaults to "".
	LastError string
}

// Lease is the repo-scoped, host-spanning exclusivity record enforcing
// "one active task per repo" (ADR-0008, ADR-0010).
type Lease struct {
	// ProjectID is the leased project; it is the lease's primary key.
	ProjectID string
	// HostID is the host holding the lease.
	HostID string
	// TaskID is the task the lease was acquired for.
	TaskID string
	// AcquiredAt is when the lease was taken; used by the reaper TTL.
	AcquiredAt time.Time
}

// Host is a self-registered executor node in the agent-per-host model (ADR-0024):
// the conductor daemon runs on EVERY host and, on startup, records its identity
// and capabilities here so capability-routing (lane.requires ⊆ host.capabilities,
// ADR-0008/2B-2) and multi-host coordination can read which hosts exist and what
// each can run. The host-spanning lease (project_id PK) already enforces
// "one active task per repo" across hosts (ADR-0008); this registry is the
// MISSING capability/liveness piece, added ADDITIVELY (ADR-0021).
//
// A host re-registers (upsert) every restart and heartbeats periodically; the
// row is observable intent + last-seen liveness, not a counter — the live lease
// table remains the source of truth for active work.
type Host struct {
	// ID is the stable identifier for the host (e.g. its hostname). It is the
	// registry primary key; RegisterHost upserts by it.
	ID string
	// Capabilities lists what this host can run (e.g. linux, backend, web, or
	// ios-build, macos, web). A lane is routable to this host only when its
	// requires are a subset of these (ADR-0008). Empty = matches only no-requires
	// lanes (backward compatible: a host with no -capabilities still registers).
	Capabilities []string
	// LastHeartbeat is when the host last reported liveness (UTC). RegisterHost and
	// HostHeartbeat advance it; a stale value lets a reader treat the host as down.
	LastHeartbeat time.Time
}

// Scenario is the intake-produced description of a task's acceptance (ADR-0005,
// ADR-0010). The hidden holdout it references lives outside the repo (ADR-0018).
type Scenario struct {
	// ID is the stable scenario identifier.
	ID string
	// ProjectID references the owning Project.
	ProjectID string
	// Title is a human-readable summary of the scenario.
	Title string
	// Lane is the capability lane the scenario targets.
	Lane string
	// Tier is the risk tier of the scenario.
	Tier string
	// Deps lists scenario/task IDs this scenario depends on.
	Deps []string
	// Acceptance holds the human-authored acceptance criteria.
	Acceptance []string
	// HoldoutRef points at the repo-external hidden holdout (ADR-0018).
	HoldoutRef string
}

// StateStore is the frozen CRUD contract over the platform's authoritative
// intent: projects, tasks, leases, and scenarios. Every method takes a
// context.Context and returns errors rather than panicking; lookups return
// ErrNotFound when no record matches.
type StateStore interface {
	// CreateProject persists a new project.
	CreateProject(ctx context.Context, p Project) error
	// GetProject returns the project by ID, or ErrNotFound.
	GetProject(ctx context.Context, id string) (Project, error)
	// ListProjects returns all registered projects.
	ListProjects(ctx context.Context) ([]Project, error)
	// UpdateProject persists changes to an existing project (by ID), or
	// ErrNotFound if no project has that ID. It is the ADDITIVE mutation seam
	// introduced by ADR-0021 (the contract grew; no existing signature changed),
	// replacing the immutable-after-create restriction that forced the ADR-0020
	// pause marker-task workaround.
	UpdateProject(ctx context.Context, p Project) error

	// CreateTask persists a new task.
	CreateTask(ctx context.Context, t Task) error
	// GetTask returns the task by ID, or ErrNotFound.
	GetTask(ctx context.Context, id string) (Task, error)
	// ListTasks returns all tasks for the given project.
	ListTasks(ctx context.Context, projectID string) ([]Task, error)
	// UpdateTask persists changes to an existing task, or ErrNotFound.
	UpdateTask(ctx context.Context, t Task) error

	// AcquireLease takes the repo-scoped lease, enforcing one active lease per
	// project; it fails if the project is already leased.
	AcquireLease(ctx context.Context, l Lease) error
	// ReleaseLease releases the lease held on the given project, unconditionally.
	// It is owner-blind by design: the reconcile reaper (B-3) legitimately
	// force-releases ANY host's stale lease with it. A holder releasing its OWN
	// lease after a reap→re-acquire must instead use the owner-scoped
	// ReleaseLeaseOwned so it cannot delete the new holder's lease (C-2).
	ReleaseLease(ctx context.Context, projectID string) error
	// ReleaseLeaseOwned releases the project's lease ONLY when it is still held by
	// the given (hostID, taskID) owner — DELETE ... WHERE project_id=$1 AND
	// host_id=$2 AND task_id=$3. It is the ADDITIVE owner-scoped release (ADR-0021;
	// no existing signature changed) the conductor uses for its own post-tick
	// release: after a false-reap and re-acquire by another host, the original
	// holder's release must NOT delete the NEW holder's lease (C-2). Deleting
	// nothing — because the caller is not (or is no longer) the owner — is a clean
	// no-op, NOT an error (idempotent), matching ReleaseLease's idempotency.
	ReleaseLeaseOwned(ctx context.Context, projectID, hostID, taskID string) error
	// GetLease returns the lease on the project, or ErrNotFound.
	GetLease(ctx context.Context, projectID string) (Lease, error)
	// ListLeases returns all currently held leases.
	ListLeases(ctx context.Context) ([]Lease, error)

	// RegisterHost upserts the host by ID (ADR-0024 self-registration): it inserts
	// a new host or, when one already exists, updates its capabilities and
	// LastHeartbeat. It is the ADDITIVE host-registry seam (ADR-0021) the
	// agent-per-host daemon calls on startup. A LastHeartbeat of zero is replaced
	// with the current time so a fresh registration is always live.
	RegisterHost(ctx context.Context, h Host) error
	// HostHeartbeat advances the host's LastHeartbeat to t (ErrNotFound if the host
	// is not registered). It is the cheap periodic liveness update separate from a
	// full RegisterHost (which also rewrites capabilities).
	HostHeartbeat(ctx context.Context, hostID string, t time.Time) error
	// GetHost returns the host by ID, or ErrNotFound.
	GetHost(ctx context.Context, id string) (Host, error)
	// ListHosts returns all registered hosts.
	ListHosts(ctx context.Context) ([]Host, error)

	// CreateScenario persists a new scenario.
	CreateScenario(ctx context.Context, s Scenario) error
	// GetScenario returns the scenario by ID, or ErrNotFound.
	GetScenario(ctx context.Context, id string) (Scenario, error)
	// ListScenarios returns all scenarios for the given project.
	ListScenarios(ctx context.Context, projectID string) ([]Scenario, error)
	// AppendScenarioAcceptance appends criteria to a scenario's acceptance list,
	// de-duplicating against the existing entries (idempotent). It lets the review
	// pipeline PERSIST a task's unresolved review findings so a later re-develop (a
	// fresh worktree, which loses .conductor/REVIEW.md) still addresses them and the
	// reviewer re-checks them — the mechanism that lets a dense screen converge across
	// autoheal retries instead of cycling forever. A no-op when criteria is empty or all
	// already present; ErrNotFound when the scenario does not exist.
	AppendScenarioAcceptance(ctx context.Context, id string, criteria []string) error
}
