package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/everva/conductor-platform/internal/intake"
	"github.com/everva/conductor-platform/internal/statestore"
)

// app bundles the injected collaborators the command handlers operate over: the
// SAME StateStore the conductor uses (no second source of truth) and the control
// seam (Faz-1b Postgres-swap). Everything is injected so handlers are testable
// against an in-memory store + fake controller, with no global mutable singleton.
type app struct {
	store statestore.StateStore
	ctrl  Controller
	out   io.Writer
}

// onboard registers a project for repo, defaulting the base branch when blank. It
// is idempotent: re-onboarding the same repo returns the existing project rather
// than duplicating it (matched on Repo). The project ID is derived from the repo
// so re-onboard is stable.
func (a *app) onboard(ctx context.Context, repo, baseBranch string) (statestore.Project, error) {
	if repo == "" {
		return statestore.Project{}, errors.New("onboard: repo is required")
	}
	if baseBranch == "" {
		baseBranch = defaultBaseBranch
	}

	// Idempotency: an existing project for the same repo is returned unchanged.
	existing, err := a.store.ListProjects(ctx)
	if err != nil {
		return statestore.Project{}, fmt.Errorf("onboard: list projects: %w", err)
	}
	for _, p := range existing {
		if p.Repo == repo {
			return p, nil
		}
	}

	p := statestore.Project{
		ID:         projectIDForRepo(repo),
		Repo:       repo,
		BaseBranch: baseBranch,
		Readiness:  "ready",
	}
	if err := a.store.CreateProject(ctx, p); err != nil {
		return statestore.Project{}, fmt.Errorf("onboard: create project: %w", err)
	}
	return p, nil
}

// intake ingests a scenario file into the ledger for projectID by DELEGATING to
// the first-class internal/intake pipeline — the ONE validated intake path. That
// package owns the rich ADR-0012 scenario schema, deterministic validation
// (required fields, T1..T4 tier, the repo-external holdout rule, dangling-dep
// resolution over the project's existing tasks AND the batch), the multi-document
// YAML loader, idempotent persistence, and the projection onto the FROZEN
// statestore types. Validation runs over the whole batch BEFORE any write, so a
// rejected intake leaves NO partial state (all-or-nothing). It persists through
// whatever StateStore conductorctl built (in-memory or the shared -dsn Postgres).
func (a *app) intake(ctx context.Context, projectID, scenarioPath string) (intake.IntakeResult, error) {
	res, err := intake.IntakeFile(ctx, a.store, projectID, scenarioPath)
	if err != nil {
		return intake.IntakeResult{}, err
	}
	return res, nil
}

// ledgerRow is one task's projection in the status summary, in a stable shape for
// both the table and the --json output.
type ledgerRow struct {
	ID     string   `json:"id"`
	Lane   string   `json:"lane"`
	Tier   string   `json:"tier"`
	Status string   `json:"status"`
	Deps   []string `json:"deps"`
	Lease  string   `json:"lease,omitempty"`
}

// status renders the ledger for projectID: per-task id/lane/tier/status/deps plus
// the lease holder, in stable task-id order. asJSON switches to machine-readable
// output. It reads only through the shared StateStore.
func (a *app) status(ctx context.Context, projectID string, asJSON bool) error {
	tasks, err := a.store.ListTasks(ctx, projectID)
	if err != nil {
		return fmt.Errorf("status: list tasks: %w", err)
	}
	// Stable ordering by task id regardless of store insertion order.
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })

	lease, leaseErr := a.store.GetLease(ctx, projectID)
	if leaseErr != nil && !errors.Is(leaseErr, statestore.ErrNotFound) {
		return fmt.Errorf("status: get lease: %w", leaseErr)
	}
	leaseTask := ""
	if leaseErr == nil {
		leaseTask = lease.TaskID
	}

	rows := make([]ledgerRow, 0, len(tasks))
	for _, t := range tasks {
		r := ledgerRow{ID: t.ID, Lane: t.Lane, Tier: t.Tier, Status: t.Status, Deps: t.Deps}
		if leaseTask == t.ID {
			r.Lease = lease.HostID
		}
		rows = append(rows, r)
	}

	if asJSON {
		enc := json.NewEncoder(a.out)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Project string      `json:"project"`
			Lease   string      `json:"lease,omitempty"`
			Tasks   []ledgerRow `json:"tasks"`
		}{Project: projectID, Lease: leaseTask, Tasks: rows})
	}

	// Render into an in-memory tabwriter, then write the aligned table to a.out in
	// a single checked write (the tabwriter buffer itself never errors).
	var buf strings.Builder
	tw := tabwriter.NewWriter(&buf, 0, 2, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "TASK\tLANE\tTIER\tSTATUS\tDEPS\tLEASE")
	for _, r := range rows {
		deps := "-"
		if len(r.Deps) > 0 {
			deps = joinDeps(r.Deps)
		}
		lease := "-"
		if r.Lease != "" {
			lease = r.Lease
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Lane, r.Tier, r.Status, deps, lease)
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("status: render table: %w", err)
	}
	if _, err := io.WriteString(a.out, buf.String()); err != nil {
		return fmt.Errorf("status: write: %w", err)
	}
	return nil
}

// pause marks the project paused via the control seam (next Tick is a no-op).
// Idempotent.
func (a *app) pause(ctx context.Context, projectID string) error {
	if projectID == "" {
		return errors.New("pause: project is required")
	}
	if err := a.ctrl.Pause(ctx, projectID); err != nil {
		return fmt.Errorf("pause: %w", err)
	}
	return nil
}

// resume clears the project's paused flag via the control seam. Idempotent.
func (a *app) resume(ctx context.Context, projectID string) error {
	if projectID == "" {
		return errors.New("resume: project is required")
	}
	if err := a.ctrl.Resume(ctx, projectID); err != nil {
		return fmt.Errorf("resume: %w", err)
	}
	return nil
}

// joinDeps renders a dep list compactly for the table.
func joinDeps(deps []string) string {
	out := ""
	for i, d := range deps {
		if i > 0 {
			out += ","
		}
		out += d
	}
	return out
}
