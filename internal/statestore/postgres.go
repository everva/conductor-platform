package statestore

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// migrationsFS embeds the goose SQL migrations so a PostgresStore can apply its
// schema without any external files at runtime (ADR-0013 single-binary host).
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// PostgresStore is the central Postgres-backed StateStore (ADR-0010, ADR-0013):
// the authoritative registry + task ledger + host-spanning lease for Faz-1b. It
// satisfies the same frozen StateStore contract as MemoryStore, with identical
// error and contention semantics (ErrNotFound, ErrAlreadyExists, ErrLeaseHeld).
//
// Lease atomicity — the host-spanning "one active lease per repo" rule
// (ADR-0008) — is enforced structurally: leases.project_id is the table's
// primary key, and AcquireLease issues a single
// `INSERT ... ON CONFLICT (project_id) DO NOTHING`. Postgres serialises
// concurrent inserts on the unique key, so among any number of racing callers
// (across hosts) exactly one row is inserted and that caller wins; every other
// caller observes zero rows affected and gets ErrLeaseHeld.
type PostgresStore struct {
	pool *pgxpool.Pool
}

// Compile-time assertion that *PostgresStore satisfies the frozen contract.
var _ StateStore = (*PostgresStore)(nil)

// NewPostgresStore opens a pgx connection pool against dsn and verifies
// connectivity with a Ping. It does NOT run migrations; call Migrate (or
// MigrateDSN) once before use. The caller owns the returned store and must call
// Close when finished.
func NewPostgresStore(ctx context.Context, dsn string) (*PostgresStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("statestore: open pgx pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("statestore: ping postgres: %w", err)
	}
	return &PostgresStore{pool: pool}, nil
}

// Close releases the underlying connection pool.
func (s *PostgresStore) Close() {
	if s.pool != nil {
		s.pool.Close()
	}
}

// Migrate applies all embedded goose migrations to the store's database, leaving
// the schema at the latest version. It is idempotent: re-running it is a no-op.
func (s *PostgresStore) Migrate(ctx context.Context) error {
	db := stdlib.OpenDBFromPool(s.pool)
	defer func() { _ = db.Close() }()
	return runGooseUp(ctx, db)
}

// MigrateDSN opens a short-lived connection to dsn, applies all embedded goose
// migrations, and closes it. It is a convenience for callers (and tests) that
// want to migrate without holding a PostgresStore.
func MigrateDSN(ctx context.Context, dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("statestore: open sql db for migrate: %w", err)
	}
	defer func() { _ = db.Close() }()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("statestore: ping for migrate: %w", err)
	}
	return runGooseUp(ctx, db)
}

// runGooseUp configures a goose provider over the embedded migrations and the
// given *sql.DB (Postgres dialect) and applies all pending Up migrations.
func runGooseUp(ctx context.Context, db *sql.DB) error {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("statestore: open embedded migrations: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, sub)
	if err != nil {
		return fmt.Errorf("statestore: build goose provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("statestore: apply migrations: %w", err)
	}
	return nil
}

// --- Projects ---------------------------------------------------------------

// CreateProject persists a new project, failing with ErrAlreadyExists if one is
// already registered under the same ID (matching MemoryStore).
func (s *PostgresStore) CreateProject(ctx context.Context, p Project) error {
	if p.ID == "" {
		return fmt.Errorf("create project: %w: empty id", ErrInvalid)
	}
	const q = `
INSERT INTO projects (id, repo, base_branch, host_id, readiness, recipe_pointer, governance_policy, paused)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (id) DO NOTHING`
	tag, err := s.pool.Exec(ctx, q,
		p.ID, p.Repo, p.BaseBranch, p.HostID, p.Readiness, p.RecipePointer, p.GovernancePolicy, p.Paused)
	if err != nil {
		return fmt.Errorf("create project %q: %w", p.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("create project %q: %w", p.ID, ErrAlreadyExists)
	}
	return nil
}

// GetProject returns the project by ID, or a wrapped ErrNotFound.
func (s *PostgresStore) GetProject(ctx context.Context, id string) (Project, error) {
	const q = `
SELECT id, repo, base_branch, host_id, readiness, recipe_pointer, governance_policy, paused
FROM projects WHERE id = $1`
	var p Project
	err := s.pool.QueryRow(ctx, q, id).Scan(
		&p.ID, &p.Repo, &p.BaseBranch, &p.HostID, &p.Readiness, &p.RecipePointer, &p.GovernancePolicy, &p.Paused)
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, fmt.Errorf("get project %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return Project{}, fmt.Errorf("get project %q: %w", id, err)
	}
	return p, nil
}

// ListProjects returns all registered projects ordered by ID.
func (s *PostgresStore) ListProjects(ctx context.Context) ([]Project, error) {
	const q = `
SELECT id, repo, base_branch, host_id, readiness, recipe_pointer, governance_policy, paused
FROM projects ORDER BY id`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	out := make([]Project, 0)
	for rows.Next() {
		var p Project
		if err := rows.Scan(
			&p.ID, &p.Repo, &p.BaseBranch, &p.HostID, &p.Readiness, &p.RecipePointer, &p.GovernancePolicy, &p.Paused); err != nil {
			return nil, fmt.Errorf("list projects: scan: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	return out, nil
}

// UpdateProject persists a full-row update to the project with the matching ID,
// or returns a wrapped ErrNotFound if no row was affected (ADR-0021 additive
// mutation; a real UPDATE, not an ON CONFLICT insert).
func (s *PostgresStore) UpdateProject(ctx context.Context, p Project) error {
	const q = `
UPDATE projects
SET repo = $2, base_branch = $3, host_id = $4, readiness = $5,
    recipe_pointer = $6, governance_policy = $7, paused = $8
WHERE id = $1`
	tag, err := s.pool.Exec(ctx, q,
		p.ID, p.Repo, p.BaseBranch, p.HostID, p.Readiness, p.RecipePointer, p.GovernancePolicy, p.Paused)
	if err != nil {
		return fmt.Errorf("update project %q: %w", p.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("update project %q: %w", p.ID, ErrNotFound)
	}
	return nil
}

// --- Tasks ------------------------------------------------------------------

// CreateTask persists a new task, failing with ErrAlreadyExists on a duplicate ID.
func (s *PostgresStore) CreateTask(ctx context.Context, t Task) error {
	if t.ID == "" {
		return fmt.Errorf("create task: %w: empty id", ErrInvalid)
	}
	requires, err := marshalStrings(t.Requires)
	if err != nil {
		return fmt.Errorf("create task %q: %w", t.ID, err)
	}
	deps, err := marshalStrings(t.Deps)
	if err != nil {
		return fmt.Errorf("create task %q: %w", t.ID, err)
	}
	const q = `
INSERT INTO tasks (id, project_id, lane, tier, status, requires, deps, branch, scenario_id, retry_count, abort_requested, approved)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (id) DO NOTHING`
	tag, err := s.pool.Exec(ctx, q,
		t.ID, t.ProjectID, t.Lane, t.Tier, t.Status, requires, deps, t.Branch, t.ScenarioID, t.RetryCount, t.AbortRequested, t.Approved)
	if err != nil {
		return fmt.Errorf("create task %q: %w", t.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("create task %q: %w", t.ID, ErrAlreadyExists)
	}
	return nil
}

// GetTask returns the task by ID, or a wrapped ErrNotFound.
func (s *PostgresStore) GetTask(ctx context.Context, id string) (Task, error) {
	const q = `
SELECT id, project_id, lane, tier, status, requires, deps, branch, scenario_id, retry_count, abort_requested, approved
FROM tasks WHERE id = $1`
	t, err := scanTask(s.pool.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, fmt.Errorf("get task %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return Task{}, fmt.Errorf("get task %q: %w", id, err)
	}
	return t, nil
}

// ListTasks returns all tasks for the given project, ordered by task ID.
func (s *PostgresStore) ListTasks(ctx context.Context, projectID string) ([]Task, error) {
	const q = `
SELECT id, project_id, lane, tier, status, requires, deps, branch, scenario_id, retry_count, abort_requested, approved
FROM tasks WHERE project_id = $1 ORDER BY id`
	rows, err := s.pool.Query(ctx, q, projectID)
	if err != nil {
		return nil, fmt.Errorf("list tasks for project %q: %w", projectID, err)
	}
	defer rows.Close()
	out := make([]Task, 0)
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("list tasks for project %q: scan: %w", projectID, err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tasks for project %q: %w", projectID, err)
	}
	return out, nil
}

// UpdateTask persists changes to an existing task, or returns a wrapped
// ErrNotFound if no task has that ID.
func (s *PostgresStore) UpdateTask(ctx context.Context, t Task) error {
	requires, err := marshalStrings(t.Requires)
	if err != nil {
		return fmt.Errorf("update task %q: %w", t.ID, err)
	}
	deps, err := marshalStrings(t.Deps)
	if err != nil {
		return fmt.Errorf("update task %q: %w", t.ID, err)
	}
	const q = `
UPDATE tasks
SET project_id = $2, lane = $3, tier = $4, status = $5, requires = $6, deps = $7,
    branch = $8, scenario_id = $9, retry_count = $10, abort_requested = $11, approved = $12
WHERE id = $1`
	tag, err := s.pool.Exec(ctx, q,
		t.ID, t.ProjectID, t.Lane, t.Tier, t.Status, requires, deps, t.Branch, t.ScenarioID, t.RetryCount, t.AbortRequested, t.Approved)
	if err != nil {
		return fmt.Errorf("update task %q: %w", t.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("update task %q: %w", t.ID, ErrNotFound)
	}
	return nil
}

// --- Leases -----------------------------------------------------------------

// AcquireLease takes the repo-scoped lease atomically, failing with ErrLeaseHeld
// if the project is already leased. Atomicity across concurrent callers (and
// hosts) is provided by the leases.project_id primary key: the single
// INSERT ... ON CONFLICT DO NOTHING is serialised by Postgres on that key, so
// exactly one racer inserts a row (wins) and the rest see zero rows affected.
func (s *PostgresStore) AcquireLease(ctx context.Context, l Lease) error {
	if l.ProjectID == "" {
		return fmt.Errorf("acquire lease: %w: empty project id", ErrInvalid)
	}
	acquiredAt := l.AcquiredAt
	if acquiredAt.IsZero() {
		acquiredAt = time.Now().UTC()
	}
	const q = `
INSERT INTO leases (project_id, host_id, task_id, acquired_at)
VALUES ($1, $2, $3, $4)
ON CONFLICT (project_id) DO NOTHING`
	tag, err := s.pool.Exec(ctx, q, l.ProjectID, l.HostID, l.TaskID, acquiredAt)
	if err != nil {
		return fmt.Errorf("acquire lease for project %q: %w", l.ProjectID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("acquire lease for project %q: %w", l.ProjectID, ErrLeaseHeld)
	}
	return nil
}

// ReleaseLease releases the lease held on the project. It is idempotent:
// releasing a project that holds no lease is not an error (matching MemoryStore).
func (s *PostgresStore) ReleaseLease(ctx context.Context, projectID string) error {
	const q = `DELETE FROM leases WHERE project_id = $1`
	if _, err := s.pool.Exec(ctx, q, projectID); err != nil {
		return fmt.Errorf("release lease for project %q: %w", projectID, err)
	}
	return nil
}

// GetLease returns the lease on the project, or a wrapped ErrNotFound.
func (s *PostgresStore) GetLease(ctx context.Context, projectID string) (Lease, error) {
	const q = `SELECT project_id, host_id, task_id, acquired_at FROM leases WHERE project_id = $1`
	var l Lease
	err := s.pool.QueryRow(ctx, q, projectID).Scan(&l.ProjectID, &l.HostID, &l.TaskID, &l.AcquiredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Lease{}, fmt.Errorf("get lease for project %q: %w", projectID, ErrNotFound)
	}
	if err != nil {
		return Lease{}, fmt.Errorf("get lease for project %q: %w", projectID, err)
	}
	return l, nil
}

// ListLeases returns all currently held leases, ordered by project ID.
func (s *PostgresStore) ListLeases(ctx context.Context) ([]Lease, error) {
	const q = `SELECT project_id, host_id, task_id, acquired_at FROM leases ORDER BY project_id`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list leases: %w", err)
	}
	defer rows.Close()
	out := make([]Lease, 0)
	for rows.Next() {
		var l Lease
		if err := rows.Scan(&l.ProjectID, &l.HostID, &l.TaskID, &l.AcquiredAt); err != nil {
			return nil, fmt.Errorf("list leases: scan: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list leases: %w", err)
	}
	return out, nil
}

// --- Scenarios --------------------------------------------------------------

// CreateScenario persists a new scenario, failing with ErrAlreadyExists on a
// duplicate ID.
func (s *PostgresStore) CreateScenario(ctx context.Context, sc Scenario) error {
	if sc.ID == "" {
		return fmt.Errorf("create scenario: %w: empty id", ErrInvalid)
	}
	deps, err := marshalStrings(sc.Deps)
	if err != nil {
		return fmt.Errorf("create scenario %q: %w", sc.ID, err)
	}
	acceptance, err := marshalStrings(sc.Acceptance)
	if err != nil {
		return fmt.Errorf("create scenario %q: %w", sc.ID, err)
	}
	const q = `
INSERT INTO scenarios (id, project_id, title, lane, tier, deps, acceptance, holdout_ref)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (id) DO NOTHING`
	tag, err := s.pool.Exec(ctx, q,
		sc.ID, sc.ProjectID, sc.Title, sc.Lane, sc.Tier, deps, acceptance, sc.HoldoutRef)
	if err != nil {
		return fmt.Errorf("create scenario %q: %w", sc.ID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("create scenario %q: %w", sc.ID, ErrAlreadyExists)
	}
	return nil
}

// GetScenario returns the scenario by ID, or a wrapped ErrNotFound.
func (s *PostgresStore) GetScenario(ctx context.Context, id string) (Scenario, error) {
	const q = `
SELECT id, project_id, title, lane, tier, deps, acceptance, holdout_ref
FROM scenarios WHERE id = $1`
	sc, err := scanScenario(s.pool.QueryRow(ctx, q, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Scenario{}, fmt.Errorf("get scenario %q: %w", id, ErrNotFound)
	}
	if err != nil {
		return Scenario{}, fmt.Errorf("get scenario %q: %w", id, err)
	}
	return sc, nil
}

// ListScenarios returns all scenarios for the given project, ordered by ID.
func (s *PostgresStore) ListScenarios(ctx context.Context, projectID string) ([]Scenario, error) {
	const q = `
SELECT id, project_id, title, lane, tier, deps, acceptance, holdout_ref
FROM scenarios WHERE project_id = $1 ORDER BY id`
	rows, err := s.pool.Query(ctx, q, projectID)
	if err != nil {
		return nil, fmt.Errorf("list scenarios for project %q: %w", projectID, err)
	}
	defer rows.Close()
	out := make([]Scenario, 0)
	for rows.Next() {
		sc, err := scanScenario(rows)
		if err != nil {
			return nil, fmt.Errorf("list scenarios for project %q: scan: %w", projectID, err)
		}
		out = append(out, sc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list scenarios for project %q: %w", projectID, err)
	}
	return out, nil
}

// --- helpers ----------------------------------------------------------------

// rowScanner abstracts pgx.Row and pgx.Rows for shared scan helpers.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanTask reads a task row, decoding the jsonb slice columns.
func scanTask(r rowScanner) (Task, error) {
	var t Task
	var requires, deps []byte
	if err := r.Scan(
		&t.ID, &t.ProjectID, &t.Lane, &t.Tier, &t.Status, &requires, &deps,
		&t.Branch, &t.ScenarioID, &t.RetryCount, &t.AbortRequested, &t.Approved); err != nil {
		return Task{}, err
	}
	var err error
	if t.Requires, err = unmarshalStrings(requires); err != nil {
		return Task{}, err
	}
	if t.Deps, err = unmarshalStrings(deps); err != nil {
		return Task{}, err
	}
	return t, nil
}

// scanScenario reads a scenario row, decoding the jsonb slice columns.
func scanScenario(r rowScanner) (Scenario, error) {
	var sc Scenario
	var deps, acceptance []byte
	if err := r.Scan(
		&sc.ID, &sc.ProjectID, &sc.Title, &sc.Lane, &sc.Tier, &deps, &acceptance,
		&sc.HoldoutRef); err != nil {
		return Scenario{}, err
	}
	var err error
	if sc.Deps, err = unmarshalStrings(deps); err != nil {
		return Scenario{}, err
	}
	if sc.Acceptance, err = unmarshalStrings(acceptance); err != nil {
		return Scenario{}, err
	}
	return sc, nil
}

// marshalStrings encodes a string slice to JSON for a jsonb column. A nil or
// empty slice is stored as the JSON empty array.
func marshalStrings(v []string) ([]byte, error) {
	if len(v) == 0 {
		return []byte("[]"), nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal string slice: %w", err)
	}
	return b, nil
}

// unmarshalStrings decodes a jsonb array into a string slice. An empty array
// decodes to nil, mirroring slices.Clone of an empty input so the Postgres and
// in-memory stores round-trip slice fields identically.
func unmarshalStrings(b []byte) ([]string, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var v []string
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, fmt.Errorf("unmarshal string slice: %w", err)
	}
	if len(v) == 0 {
		return nil, nil
	}
	return v, nil
}
