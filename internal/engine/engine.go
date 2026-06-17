// Package engine defines the frozen engine contract: the five-verb
// EngineAdapter the platform skeleton sees (ADR-0002), the evidence-based
// Verdict and ReviewResult it exchanges (ADR-0003, ADR-0014), and the sentinel
// errors that encode LLM-output resilience (ADR-0014).
//
// The platform never learns engine internals: it asks the adapter to develop or
// verify a task and normalizes the result. A single generic CommandEngine
// implements this by running recipe commands as subprocesses; the development
// pipeline runs inside the performer, not in Go (ADR-0002, ADR-0014).
//
// This contract is FROZEN: later tasks fill the implementation behind it but
// must not change the signatures, the Verdict/Check/ReviewResult field sets, or
// the sentinel error names.
package engine

import (
	"context"
	"errors"
	"time"

	"github.com/everva/conductor-platform/internal/statestore"
)

// Sentinel errors for performer-output resilience (ADR-0014). ADR-0014 maps
// directly onto these names, so they are part of the frozen contract. Callers
// detect them with errors.Is; producers wrap them with %w.
var (
	// ErrMalformedVerdict means the performer emitted output that could not be
	// parsed into a Verdict; the task is blocked, never silently retried.
	ErrMalformedVerdict = errors.New("engine: malformed verdict")
	// ErrNoVerdict means the performer produced no verdict at all (timeout or
	// empty output); the task is blocked.
	ErrNoVerdict = errors.New("engine: no verdict")
	// ErrAuthExpired means the performer hit an auth wall ("Not logged in" /
	// token expiry); the whole tick must stop and notify rather than retry.
	ErrAuthExpired = errors.New("engine: auth expired")
)

// Workspace is the isolated checkout a performer operates in: a per-task git
// worktree on a short-lived branch (ADR-0017).
type Workspace struct {
	// Path is the absolute filesystem path of the worktree.
	Path string
	// Branch is the short-lived per-task branch checked out there (ADR-0004).
	Branch string
}

// Session identifies a running performer process for liveness probing
// (ADR-0006).
type Session struct {
	// ID is the platform's handle for the running performer.
	ID string
	// TaskID is the task the session is working on.
	TaskID string
}

// HealthState is the deterministic liveness signal the sentinel's Layer-1
// consumes (ADR-0006).
type HealthState struct {
	// Phase is the pipeline phase the performer last reported.
	Phase string
	// LastActivityTS is when output last flowed from the performer.
	LastActivityTS time.Time
	// Signal is the coarse liveness verdict: progressing, idle, or unknown.
	Signal string
}

// Event is the normalized observability envelope the engine emits from its
// subprocess output (ADR-0011). The engine only normalizes; persisting and
// NOTIFY-ing is the platform's job.
type Event struct {
	// TS is when the event occurred.
	TS time.Time
	// Project is the project identifier the event belongs to.
	Project string
	// Task is the task identifier the event belongs to.
	Task string
	// Phase is one of plan, develop, test, review, verify, merge (ADR-0011).
	Phase string
	// Kind is one of started, progress, log, diff, decision, health, pr, merge,
	// intervention-needed (ADR-0011).
	Kind string
	// Payload carries kind-specific structured data.
	Payload map[string]any
}

// Command is a control directive the platform sends to a running engine
// (ADR-0002, ADR-0011).
type Command struct {
	// Action is one of pause, resume, abort.
	Action string
}

// Check is one piece of deterministic evidence in a Verdict — a single gate
// command and its outcome (ADR-0003). The set of checks is recipe-driven
// (unit, lint, build, maestro, visual, …) so the shape is generic.
type Check struct {
	// Name is the gate's name (e.g. "go build", "go test").
	Name string `json:"name"`
	// Result is the gate outcome: pass or fail.
	Result string `json:"result"`
	// Evidence is a short, factual record of the outcome.
	Evidence string `json:"evidence"`
}

// Verdict is the schema-forced result a performer emits and the platform parses
// (ADR-0002, ADR-0014). Its JSON shape is frozen and pinned by the hidden
// holdout (ADR-0018); the field set must not drift.
type Verdict struct {
	// Result is the overall outcome: pass, fail, or blocked.
	Result string `json:"result"`
	// Branch is the per-task branch the work landed on.
	Branch string `json:"branch"`
	// CommitSHA is the commit on that branch, or empty if none was made.
	CommitSHA string `json:"commit_sha"`
	// Checks is the deterministic evidence backing the result.
	Checks []Check `json:"checks"`
	// Files lists the paths created or modified.
	Files []string `json:"files"`
	// BlockedReason explains a blocked result; empty otherwise.
	BlockedReason string `json:"blocked_reason"`
	// Summary is a short, factual description of what happened.
	Summary string `json:"summary"`
}

// ReviewResult is the outcome of the independent fresh-eyes review (ADR-0003):
// an evidence-based pass/changes-requested decision, never a subjective score.
type ReviewResult struct {
	// Result is the review decision: pass or changes-requested.
	Result string `json:"result"`
	// Findings lists concrete issues the reviewer found.
	Findings []string `json:"findings"`
	// Summary is a short, factual description of the review.
	Summary string `json:"summary"`
}

// EngineAdapter is the frozen five-verb interface the platform skeleton sees
// (ADR-0002). The platform selects the task; the engine only develops/verifies
// it and reports liveness, events, and accepts control commands.
type EngineAdapter interface {
	// Develop runs the in-performer pipeline to produce code and a local commit
	// for the task, returning the resulting Verdict (ADR-0004, ADR-0014).
	Develop(ctx context.Context, task statestore.Task, ws Workspace) (Verdict, error)
	// Verify runs the independent fresh-eyes review plus deterministic evidence
	// over a prior Verdict, returning a ReviewResult (ADR-0003).
	Verify(ctx context.Context, verdict Verdict, ws Workspace) (ReviewResult, error)
	// Health returns the Layer-1 liveness signal for a running session (ADR-0006).
	Health(ctx context.Context, session Session) (HealthState, error)
	// Events returns a channel of normalized events from the engine (ADR-0011).
	Events(ctx context.Context) (<-chan Event, error)
	// Control delivers a pause/resume/abort command to the engine (ADR-0002).
	Control(ctx context.Context, cmd Command) error
}
