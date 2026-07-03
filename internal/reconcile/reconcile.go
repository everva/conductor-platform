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
	"strings"
	"time"

	"github.com/everva/conductor-platform/internal/statestore"
)

// statusDone is the terminal task status written when a merge is observed. It
// matches the registry's canonical value (ADR-0004) without importing the
// registry package, keeping the backstop decoupled (ADR-0016).
const statusDone = "done"

// statusBlocked and statusReady are the canonical registry values (ADR-0004) the
// transient-block auto-retry transitions between (blocked→ready). Inlined here —
// like statusDone — to keep the backstop decoupled from the registry package.
const (
	statusBlocked = "blocked"
	statusReady   = "ready"
)

// transientBlockSignatures are the (lower-cased) LastError substrings that mark a
// block as TRANSIENT: the developer produced no usable diff/verdict (a claude
// hiccup) or the run was aborted by infra, NOT a real gate/review failure. Only
// these are auto-retried by RetryTransientBlocked; a descriptive gate failure
// (e.g. "i18n parity failed…") is deliberately left blocked for a human. Keep this
// list conservative — matching too broadly would re-run expensive develops on real
// failures that genuinely need attention.
var transientBlockSignatures = []string{
	"no result-keyed json",
	"malformed verdict",
	"made no change",
	"no change was made",
	"developer made no change",
	"signal: terminated",
	"terminated signal",
	"context deadline exceeded",
}

// isTransientBlock reports whether a blocked task's LastError marks a no-output /
// infra-abort block that is safe to auto-retry. An EMPTY LastError counts as
// transient: a real gate/review failure always carries a reason, so a blocked task
// with no reason is a no-output hiccup.
func isTransientBlock(lastError string) bool {
	le := strings.ToLower(strings.TrimSpace(lastError))
	if le == "" {
		return true
	}
	for _, sig := range transientBlockSignatures {
		if strings.Contains(le, sig) {
			return true
		}
	}
	return false
}

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

// RetryTransientBlocked is the third reconcile operation (after ReapLeases and
// ReconcileTasks): it re-queues tasks that blocked for a TRANSIENT / no-output
// reason so the fleet self-heals a claude hiccup instead of stalling until a human
// hits /retry (ADR-0004 §max-retries, operationalized for the gateway-mediated
// path where handleAgentResult persists blocked but never auto-retries). For each
// task of the project that is `blocked` with a transient LastError
// (isTransientBlock) AND whose RetryCount is below max, it transitions
// blocked→ready and increments RetryCount. The cap is the loop-breaker: once
// RetryCount reaches max the task stays blocked for a human, so a genuinely
// unbuildable task cannot cycle forever. It is deterministic and idempotent — a
// task at the cap, or blocked for a real gate/review reason, is left untouched —
// and returns the number of tasks re-queued. A max <= 0 disables auto-retry.
func (r *Reconciler) RetryTransientBlocked(ctx context.Context, project statestore.Project, max int) (int, error) {
	if project.ID == "" {
		return 0, fmt.Errorf("reconcile: %w", errors.New("project ID is required"))
	}
	if max <= 0 {
		return 0, nil // auto-retry disabled
	}
	tasks, err := r.store.ListTasks(ctx, project.ID)
	if err != nil {
		return 0, fmt.Errorf("reconcile: list tasks for %q: %w", project.ID, err)
	}
	retried := 0
	for _, t := range tasks {
		if t.Status != statusBlocked {
			continue
		}
		if t.RetryCount >= max {
			continue // cap reached — leave blocked for a human (loop-breaker)
		}
		if !isTransientBlock(t.LastError) {
			continue // real gate/review failure — not auto-retryable
		}
		t.Status = statusReady
		t.RetryCount++
		t.LastError = "" // clear so the board shows it retrying clean (mirrors handleRetry)
		if err := r.store.UpdateTask(ctx, t); err != nil {
			return retried, fmt.Errorf("reconcile: re-queue transient-blocked task %q: %w", t.ID, err)
		}
		retried++
	}
	return retried, nil
}

// HostHeartbeatOwnerLive builds an OwnerLive predicate for Config.OwnerLive that
// reports a lease's owner DEAD when the owning HOST's last heartbeat is stale
// (ADR-0024 agent-per-host, 2B-3). In a multi-host deployment PID-liveness is
// meaningless across machines, so cross-host stale-lease reaping must be driven
// by the host registry's LastHeartbeat instead: a host that stopped heartbeating
// is treated as down and its lease becomes reapable, freeing the repo for another
// capable host. A host whose heartbeat is FRESH (within hostStale of `now`) is
// kept LIVE so a working host's lease is never wrongly reaped.
//
// Semantics (deterministic; `now` is injected, never read from the wall clock):
//   - Owner host has a fresh heartbeat (now - LastHeartbeat <= hostStale) → LIVE
//     (returns true): the lease is NOT reaped by this predicate.
//   - Owner host's heartbeat is stale (now - LastHeartbeat > hostStale) → DEAD
//     (returns false): the lease is reapable.
//   - Owner host has NO registry row (GetHost → ErrNotFound) → treated as
//     DEAD/unknown → reapable (returns false), CONSERVATIVELY: an unregistered or
//     forgotten host cannot prove liveness, so its lease must not pin the repo
//     forever. The TTL backstop (Config.LeaseTTL) still applies independently, so
//     even a host that briefly disappears is bounded by both seams.
//   - A store error other than ErrNotFound → treated as LIVE (returns true) so a
//     transient lookup failure does NOT cause a spurious reap; the TTL backstop
//     still bounds a truly dead lease.
//
// hostStale must be > 0; a non-positive threshold treats EVERY registered host as
// dead (now - LastHeartbeat > 0 for any past heartbeat), which is almost never
// intended, so callers should pass a real threshold (e.g. several heartbeat
// intervals). The returned predicate looks the host up through the store on every
// call (no caching), so it reflects the registry as of the pass.
func HostHeartbeatOwnerLive(ctx context.Context, store statestore.StateStore, hostStale time.Duration, now time.Time) func(l statestore.Lease) bool {
	return func(l statestore.Lease) bool {
		h, err := store.GetHost(ctx, l.HostID)
		if err != nil {
			if errors.Is(err, statestore.ErrNotFound) {
				return false // unknown/unregistered host → conservatively dead → reapable.
			}
			return true // transient lookup error → keep live; TTL backstop still bounds it.
		}
		age := now.Sub(h.LastHeartbeat)
		return age <= hostStale // fresh heartbeat → live; stale → dead → reapable.
	}
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
