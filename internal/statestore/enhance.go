package statestore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// EnhanceJob is an intake "enhance" request: the director writes a rough request (Turkish)
// and clicks Enhance; the gateway records a pending job, an AGENT (which has the project's
// repo cloned + claude) claims it, runs claude read-only over the real code, and writes back
// a detailed Turkish spec. The editor polls GetEnhanceJob and fills the composer.
//
// ADDITIVE (ADR-0021): this type + the EnhanceStore seam are SEPARATE from the frozen
// StateStore, reached by an optional type-assertion, so no existing implementer/fake breaks.
type EnhanceJob struct {
	// ID is the unique job id (caller-supplied, stable).
	ID string
	// ProjectID scopes the job (the agent claims per project; the enhance reads that repo).
	ProjectID string
	// RoughSpec is the director's raw request text (any language; the enhance outputs Turkish).
	RoughSpec string
	// Status is the lifecycle: pending → running (agent claimed) → done | failed.
	Status string
	// Result is the enhanced Turkish spec (set when Status == done).
	Result string
	// Error is a short, secret-free failure reason (set when Status == failed).
	Error string
	// CreatedAt orders the claim (oldest pending first). Set by the store on create.
	CreatedAt time.Time
}

// Enhance job statuses.
const (
	EnhancePending = "pending"
	EnhanceRunning = "running"
	EnhanceDone    = "done"
	EnhanceFailed  = "failed"
)

// EnhanceStore is the OPTIONAL narrow persistence seam for intake enhance jobs, kept SEPARATE
// from the frozen StateStore (ADR-0021 additive). Both PostgresStore and MemoryStore implement
// it; the gateway type-asserts it for the control + agent-API endpoints (501 if absent).
type EnhanceStore interface {
	// CreateEnhanceJob inserts a new pending job. The id must be unique.
	CreateEnhanceJob(ctx context.Context, j EnhanceJob) error
	// GetEnhanceJob returns the job by id, or ErrNotFound.
	GetEnhanceJob(ctx context.Context, id string) (EnhanceJob, error)
	// ClaimEnhanceJob atomically moves the OLDEST pending job for the project to running and
	// returns it. ok=false (no error) when there is nothing pending.
	ClaimEnhanceJob(ctx context.Context, projectID string) (job EnhanceJob, ok bool, err error)
	// CompleteEnhanceJob finishes a running job: errMsg=="" → done with result; else → failed.
	CompleteEnhanceJob(ctx context.Context, id, result, errMsg string) error
}

// Compile-time assertions that both stores satisfy the additive seam.
var (
	_ EnhanceStore = (*MemoryStore)(nil)
	_ EnhanceStore = (*PostgresStore)(nil)
)

// ── MemoryStore ───────────────────────────────────────────────────────────────────────

// CreateEnhanceJob inserts a pending job (last write wins per id; CreatedAt stamped if unset).
func (s *MemoryStore) CreateEnhanceJob(ctx context.Context, j EnhanceJob) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if j.ID == "" || j.ProjectID == "" {
		return fmt.Errorf("create enhance job: %w: empty id/project", ErrInvalid)
	}
	if j.Status == "" {
		j.Status = EnhancePending
	}
	if j.CreatedAt.IsZero() {
		j.CreatedAt = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enhance[j.ID] = j
	return nil
}

// GetEnhanceJob returns the job by id, or a wrapped ErrNotFound.
func (s *MemoryStore) GetEnhanceJob(ctx context.Context, id string) (EnhanceJob, error) {
	if err := ctx.Err(); err != nil {
		return EnhanceJob{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.enhance[id]
	if !ok {
		return EnhanceJob{}, fmt.Errorf("get enhance job %q: %w", id, ErrNotFound)
	}
	return j, nil
}

// ClaimEnhanceJob moves the oldest pending job for the project to running and returns it.
func (s *MemoryStore) ClaimEnhanceJob(ctx context.Context, projectID string) (EnhanceJob, bool, error) {
	if err := ctx.Err(); err != nil {
		return EnhanceJob{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *EnhanceJob
	for id, j := range s.enhance {
		if j.ProjectID != projectID || j.Status != EnhancePending {
			continue
		}
		if best == nil || j.CreatedAt.Before(best.CreatedAt) {
			jc := s.enhance[id]
			best = &jc
		}
	}
	if best == nil {
		return EnhanceJob{}, false, nil
	}
	best.Status = EnhanceRunning
	s.enhance[best.ID] = *best
	return *best, true, nil
}

// CompleteEnhanceJob finishes a running job (done with result, or failed with errMsg).
func (s *MemoryStore) CompleteEnhanceJob(ctx context.Context, id, result, errMsg string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.enhance[id]
	if !ok {
		return fmt.Errorf("complete enhance job %q: %w", id, ErrNotFound)
	}
	if errMsg != "" {
		j.Status, j.Error, j.Result = EnhanceFailed, errMsg, ""
	} else {
		j.Status, j.Result, j.Error = EnhanceDone, result, ""
	}
	s.enhance[id] = j
	return nil
}

// ── PostgresStore ─────────────────────────────────────────────────────────────────────

// CreateEnhanceJob inserts a pending job.
func (s *PostgresStore) CreateEnhanceJob(ctx context.Context, j EnhanceJob) error {
	if j.ID == "" || j.ProjectID == "" {
		return fmt.Errorf("create enhance job: %w: empty id/project", ErrInvalid)
	}
	const q = `INSERT INTO enhance_jobs (id, project_id, rough_spec, status)
VALUES ($1, $2, $3, 'pending')`
	if _, err := s.pool.Exec(ctx, q, j.ID, j.ProjectID, j.RoughSpec); err != nil {
		return fmt.Errorf("create enhance job %q: %w", j.ID, err)
	}
	return nil
}

// GetEnhanceJob returns the job by id, or a wrapped ErrNotFound.
func (s *PostgresStore) GetEnhanceJob(ctx context.Context, id string) (EnhanceJob, error) {
	const q = `SELECT id, project_id, rough_spec, status, result, error, created_at FROM enhance_jobs WHERE id = $1`
	var j EnhanceJob
	err := s.pool.QueryRow(ctx, q, id).Scan(&j.ID, &j.ProjectID, &j.RoughSpec, &j.Status, &j.Result, &j.Error, &j.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return EnhanceJob{}, fmt.Errorf("get enhance job %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return EnhanceJob{}, fmt.Errorf("get enhance job %q: %w", id, err)
	}
	return j, nil
}

// ClaimEnhanceJob atomically claims the oldest pending job for the project (FOR UPDATE SKIP
// LOCKED so concurrent agents never grab the same one).
func (s *PostgresStore) ClaimEnhanceJob(ctx context.Context, projectID string) (EnhanceJob, bool, error) {
	const q = `
UPDATE enhance_jobs SET status = 'running', updated_at = now()
WHERE id = (
  SELECT id FROM enhance_jobs
  WHERE project_id = $1 AND status = 'pending'
  ORDER BY created_at ASC
  FOR UPDATE SKIP LOCKED
  LIMIT 1
)
RETURNING id, project_id, rough_spec, status, result, error, created_at`
	var j EnhanceJob
	err := s.pool.QueryRow(ctx, q, projectID).Scan(&j.ID, &j.ProjectID, &j.RoughSpec, &j.Status, &j.Result, &j.Error, &j.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return EnhanceJob{}, false, nil
	}
	if err != nil {
		return EnhanceJob{}, false, fmt.Errorf("claim enhance job %q: %w", projectID, err)
	}
	return j, true, nil
}

// CompleteEnhanceJob finishes a running job (done with result, or failed with errMsg).
func (s *PostgresStore) CompleteEnhanceJob(ctx context.Context, id, result, errMsg string) error {
	status, res, errCol := EnhanceDone, result, ""
	if errMsg != "" {
		status, res, errCol = EnhanceFailed, "", errMsg
	}
	const q = `UPDATE enhance_jobs SET status = $2, result = $3, error = $4, updated_at = now() WHERE id = $1`
	tag, err := s.pool.Exec(ctx, q, id, status, res, errCol)
	if err != nil {
		return fmt.Errorf("complete enhance job %q: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("complete enhance job %q: %w", id, ErrNotFound)
	}
	return nil
}
