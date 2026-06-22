package statestore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// TaskDiff is the persisted FULL-context diff of a task's verified branch vs the project base
// (Faz-P P2b, ADR-0041). It is stored OUT-OF-BAND from the bounded KindDiff event: the event
// (events.DiffSummary) is capped at ~8KB for the Postgres LISTEN/NOTIFY bus, which is too small
// to carry whole-file content, so the editor's full-file NATIVE diff fetches this richer record
// instead. The conductor upserts one row per (project, task) at the green gate; the editor's
// host-side client GETs it and reconstructs full-file before/after for vscode.diff.
//
// ADDITIVE (ADR-0021): this type and the TaskDiffStore seam below are SEPARATE from the frozen
// StateStore interface, so no existing implementer or fake breaks — the persistence is reached
// by an optional type-assertion (a store without it simply means the editor falls back to the
// bounded KindDiff patch).
type TaskDiff struct {
	// ProjectID + TaskID are the composite key (one stored diff per task; re-diffs upsert).
	ProjectID string
	TaskID    string
	// Base / Branch are the diff range endpoints (project base branch ... task branch).
	Base   string
	Branch string
	// Patch is the FULL-context unified patch (git diff --unified=<huge>): each changed file's
	// entire content as context, so the editor reconstructs whole-file before/after. Capped at a
	// generous byte budget by the producer (much larger than the NOTIFY-bounded event patch).
	Patch string
	// Truncated is true when the producer capped the full patch to its byte budget.
	Truncated bool
}

// TaskDiffStore is the OPTIONAL narrow persistence seam for the P2b full-file diff (ADR-0041),
// kept SEPARATE from the frozen StateStore (ADR-0021 additive). Both PostgresStore and
// MemoryStore implement it; the conductor type-asserts it for a best-effort persist at the gate,
// and the gateway type-asserts it to serve GET /projects/{id}/tasks/{task}/diff (501 if absent).
type TaskDiffStore interface {
	// PutTaskDiff upserts the task's full-context diff (by project_id + task_id).
	PutTaskDiff(ctx context.Context, d TaskDiff) error
	// GetTaskDiff returns the task's stored full-context diff, or ErrNotFound when none is
	// stored (never emitted, or pre-dating P2b) — the editor then uses the bounded event patch.
	GetTaskDiff(ctx context.Context, projectID, taskID string) (TaskDiff, error)
}

// Compile-time assertions that both stores satisfy the additive seam.
var (
	_ TaskDiffStore = (*MemoryStore)(nil)
	_ TaskDiffStore = (*PostgresStore)(nil)
)

// taskDiffKey composes the in-memory map key from the (project, task) pair. The NUL separator
// can't appear in an id, so distinct pairs never collide.
func taskDiffKey(projectID, taskID string) string {
	return projectID + "\x00" + taskID
}

// PutTaskDiff upserts the task's full-context diff in memory (last write wins per task).
func (s *MemoryStore) PutTaskDiff(ctx context.Context, d TaskDiff) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.ProjectID == "" || d.TaskID == "" {
		return fmt.Errorf("put task diff: %w: empty project/task id", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.taskDiffs[taskDiffKey(d.ProjectID, d.TaskID)] = d
	return nil
}

// GetTaskDiff returns the stored full-context diff for the (project, task) pair, or a wrapped
// ErrNotFound when none is stored.
func (s *MemoryStore) GetTaskDiff(ctx context.Context, projectID, taskID string) (TaskDiff, error) {
	if err := ctx.Err(); err != nil {
		return TaskDiff{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.taskDiffs[taskDiffKey(projectID, taskID)]
	if !ok {
		return TaskDiff{}, fmt.Errorf("get task diff %q/%q: %w", projectID, taskID, ErrNotFound)
	}
	return d, nil
}

// PutTaskDiff upserts the task's full-context diff in Postgres (INSERT ... ON CONFLICT on the
// (project_id, task_id) primary key), mirroring RegisterHost's upsert shape.
func (s *PostgresStore) PutTaskDiff(ctx context.Context, d TaskDiff) error {
	if d.ProjectID == "" || d.TaskID == "" {
		return fmt.Errorf("put task diff: %w: empty project/task id", ErrInvalid)
	}
	const q = `
INSERT INTO task_diffs (project_id, task_id, base, branch, patch, truncated, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, now())
ON CONFLICT (project_id, task_id) DO UPDATE SET
  base = EXCLUDED.base, branch = EXCLUDED.branch, patch = EXCLUDED.patch,
  truncated = EXCLUDED.truncated, updated_at = now()`
	if _, err := s.pool.Exec(ctx, q, d.ProjectID, d.TaskID, d.Base, d.Branch, d.Patch, d.Truncated); err != nil {
		return fmt.Errorf("put task diff %q/%q: %w", d.ProjectID, d.TaskID, err)
	}
	return nil
}

// GetTaskDiff returns the stored full-context diff for the (project, task) pair, or a wrapped
// ErrNotFound when no row exists.
func (s *PostgresStore) GetTaskDiff(ctx context.Context, projectID, taskID string) (TaskDiff, error) {
	const q = `SELECT project_id, task_id, base, branch, patch, truncated FROM task_diffs WHERE project_id = $1 AND task_id = $2`
	var d TaskDiff
	err := s.pool.QueryRow(ctx, q, projectID, taskID).Scan(
		&d.ProjectID, &d.TaskID, &d.Base, &d.Branch, &d.Patch, &d.Truncated,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return TaskDiff{}, fmt.Errorf("get task diff %q/%q: %w", projectID, taskID, ErrNotFound)
	}
	if err != nil {
		return TaskDiff{}, fmt.Errorf("get task diff %q/%q: %w", projectID, taskID, err)
	}
	return d, nil
}
