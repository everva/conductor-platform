// Package reconcile is the independent, DETERMINISTIC recovery backstop of
// Faz-1a (ADR-0016, operationalizing ADR-0006 Katman-1/3). It is pure Go with
// NO LLM (no `claude -p`): a stale-lease reaper plus a git-derived task
// reconcile. It is designed to run as a SEPARATE job from the conductor loop so
// the conductor can recover from its own death; it depends only on the frozen
// statestore.StateStore interface plus a small injected git-reader and clock, so
// it survives the death of the engine/verify/loop packages.
//
// Runtime truth is DERIVED from git every pass and never cached (ADR-0010 §3):
// a squash-merge commit on the base branch carrying an EXACT `[task:<id>]`
// trailer means that task is merged -> done. Matching is exact, never
// fuzzy/substring (ADR-0004 §Güncelleme). Reap and reconcile are idempotent:
// the same inputs yield the same state with no duplicate writes (ADR-0016).
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/everva/conductor-platform/internal/statestore"
)

// statusDone is the terminal task status written when a merge is observed. It
// matches the registry's canonical value (ADR-0004) without importing the
// registry package, keeping the backstop decoupled (ADR-0016).
const statusDone = "done"

// Commit is one base-branch commit as seen by the git reader. Trailers are the
// parsed git trailer lines (e.g. "[task:B-3]"); the reconciler matches them
// exactly, so the reader is responsible only for surfacing them, not matching.
type Commit struct {
	// SHA is the commit hash.
	SHA string
	// Subject is the commit subject line.
	Subject string
	// Trailers are the commit's trailer lines, verbatim.
	Trailers []string
}

// GitReader derives observed truth from git. It is injected so tests use a fake
// log and the reconciler never shells out itself, keeping it decoupled from any
// live repo (ADR-0016). Implementations must return commits on the project's
// base branch.
type GitReader interface {
	// BaseCommits returns the commits on the project's base branch, newest or
	// oldest order is irrelevant since matching is per-commit and idempotent.
	BaseCommits(ctx context.Context, project statestore.Project) ([]Commit, error)
}

// Config tunes the reconciler. All time comes from the injected `now` passed to
// ReapLeases — there is no hidden wall-clock — so runs are deterministic.
type Config struct {
	// LeaseTTL is how old a lease may be before the reaper releases it.
	LeaseTTL time.Duration
	// OwnerLive reports whether a lease's owner is still alive (e.g. PID/host
	// liveness). A nil OwnerLive treats every owner as alive, so only TTL applies.
	OwnerLive func(l statestore.Lease) bool
}

// Reconciler is the deterministic backstop over the frozen StateStore and an
// injected GitReader. Construct it with New and pass the store/reader explicitly
// — there is no global singleton, and it holds no LLM logic.
type Reconciler struct {
	store statestore.StateStore
	git   GitReader
	cfg   Config
}

// New returns a Reconciler over the given store, git reader, and config.
func New(store statestore.StateStore, git GitReader, cfg Config) *Reconciler {
	return &Reconciler{store: store, git: git, cfg: cfg}
}

// ReapLeases releases every lease that is stale — older than the configured TTL
// relative to the injected `now`, OR whose owner the injected OwnerLive reports
// dead (ADR-0010 §reaper, ADR-0016). A lease that is fresh and live is left
// untouched. It is idempotent: an already-released lease is not re-released
// (ReleaseLease is only called for leases still present this pass), and a lease
// re-acquired AFTER `now` (AcquiredAt in the future) is treated as fresh.
func (r *Reconciler) ReapLeases(ctx context.Context, now time.Time) error {
	leases, err := r.store.ListLeases(ctx)
	if err != nil {
		return fmt.Errorf("reconcile: list leases: %w", err)
	}
	for _, l := range leases {
		if !r.isStale(l, now) {
			continue
		}
		if err := r.store.ReleaseLease(ctx, l.ProjectID); err != nil {
			return fmt.Errorf("reconcile: reap lease %q: %w", l.ProjectID, err)
		}
	}
	return nil
}

// isStale reports whether a lease should be reaped: its age exceeds the TTL, or
// its owner is dead. A lease acquired in the future relative to `now` (e.g.
// re-acquired just after the snapshot) has non-positive age and is never TTL-
// stale, so the reaper does not race a fresh re-acquire.
func (r *Reconciler) isStale(l statestore.Lease, now time.Time) bool {
	if r.cfg.OwnerLive != nil && !r.cfg.OwnerLive(l) {
		return true
	}
	if r.cfg.LeaseTTL <= 0 {
		return false
	}
	age := now.Sub(l.AcquiredAt)
	return age > r.cfg.LeaseTTL
}

// ReconcileTasks derives merged tasks from git and marks them done (ADR-0004,
// ADR-0010, ADR-0016). For each base-branch commit carrying an EXACT
// `[task:<id>]` trailer matching a known task of the project, it sets that
// task's status to done via UpdateTask. Matching is exact (no substring/fuzzy),
// truth is re-derived from git each pass (never cached), and the operation is
// idempotent: a task already done is not written again, and an unknown or
// malformed trailer flips nothing.
func (r *Reconciler) ReconcileTasks(ctx context.Context, project statestore.Project) error {
	if project.ID == "" {
		return fmt.Errorf("reconcile: %w", errors.New("project ID is required"))
	}
	commits, err := r.git.BaseCommits(ctx, project)
	if err != nil {
		return fmt.Errorf("reconcile: base commits for %q: %w", project.ID, err)
	}

	// Collect the set of merged task IDs from exact trailer matches.
	merged := make(map[string]struct{})
	for _, c := range commits {
		for _, tr := range c.Trailers {
			if id, ok := parseTaskTrailer(tr); ok {
				merged[id] = struct{}{}
			}
		}
	}
	if len(merged) == 0 {
		return nil
	}

	tasks, err := r.store.ListTasks(ctx, project.ID)
	if err != nil {
		return fmt.Errorf("reconcile: list tasks for %q: %w", project.ID, err)
	}
	for _, t := range tasks {
		if _, ok := merged[t.ID]; !ok {
			continue
		}
		if t.Status == statusDone {
			continue // already done -> no duplicate write (idempotent).
		}
		t.Status = statusDone
		if err := r.store.UpdateTask(ctx, t); err != nil {
			return fmt.Errorf("reconcile: mark task %q done: %w", t.ID, err)
		}
	}
	return nil
}

// parseTaskTrailer extracts the task ID from an EXACT `[task:<id>]` trailer
// (ADR-0004 §Güncelleme). The whole trailer must be exactly "[task:<id>]" with a
// non-empty id and no surrounding noise, so a malformed or decorated trailer
// (e.g. "[task:T-1-extra]" is its own distinct id, but "see [task:T-1] below"
// does not match) never flips a task. It returns ok=false when not an exact
// match.
func parseTaskTrailer(trailer string) (string, bool) {
	const prefix = "[task:"
	const suffix = "]"
	if len(trailer) <= len(prefix)+len(suffix) {
		return "", false
	}
	if trailer[:len(prefix)] != prefix || trailer[len(trailer)-len(suffix):] != suffix {
		return "", false
	}
	id := trailer[len(prefix) : len(trailer)-len(suffix)]
	if id == "" {
		return "", false
	}
	return id, true
}
