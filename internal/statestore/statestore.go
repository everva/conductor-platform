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
	// ReleaseLease releases the lease held on the given project.
	ReleaseLease(ctx context.Context, projectID string) error
	// GetLease returns the lease on the project, or ErrNotFound.
	GetLease(ctx context.Context, projectID string) (Lease, error)
	// ListLeases returns all currently held leases.
	ListLeases(ctx context.Context) ([]Lease, error)

	// CreateScenario persists a new scenario.
	CreateScenario(ctx context.Context, s Scenario) error
	// GetScenario returns the scenario by ID, or ErrNotFound.
	GetScenario(ctx context.Context, id string) (Scenario, error)
	// ListScenarios returns all scenarios for the given project.
	ListScenarios(ctx context.Context, projectID string) ([]Scenario, error)
}
