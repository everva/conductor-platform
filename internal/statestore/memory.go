package statestore

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// ErrAlreadyExists is returned by the Create* verbs when a record with the same
// identifier already exists. It is additive to the frozen contract (the
// interface methods return error) and lets callers distinguish a conflict from
// other failures via errors.Is.
var ErrAlreadyExists = fmt.Errorf("statestore: record already exists")

// ErrLeaseHeld is returned by AcquireLease when the project already has an
// active lease; it enforces the single-active-task-per-repo invariant
// (ADR-0008, ADR-0010).
var ErrLeaseHeld = fmt.Errorf("statestore: lease already held")

// ErrInvalid is returned when a record is missing a required identifier.
var ErrInvalid = fmt.Errorf("statestore: invalid record")

// MemoryStore is the in-memory StateStore implementation used in Faz-1a
// (ADR-0013). It is safe for concurrent use: all access is guarded by a single
// RWMutex, and every stored or returned value is a deep copy so a caller
// mutating a struct it passed in or received back cannot corrupt the store.
//
// It implements the frozen StateStore interface and is meant to be created with
// NewMemoryStore and passed explicitly to callers — there is no global
// singleton. The Faz-1b Postgres store replaces it behind the same interface.
type MemoryStore struct {
	mu        sync.RWMutex
	projects  map[string]Project
	tasks     map[string]Task
	leases    map[string]Lease // keyed by ProjectID
	scenarios map[string]Scenario
	hosts     map[string]Host       // keyed by Host.ID (ADR-0024 registry)
	taskDiffs map[string]TaskDiff   // keyed by taskDiffKey(projectID, taskID) (P2b, ADR-0041)
	creds     map[string]Credential // keyed by Credential.Kind (L3, ADR-0049)
	enhance   map[string]EnhanceJob // keyed by EnhanceJob.ID (intake enhance, agent-side)
	// intakeSessions holds persisted intake conversations keyed by IntakeSession.ID (history).
	intakeSessions map[string]IntakeSession
	// usage holds the latest claude-subscription utilization snapshot per key (UsageStore seam).
	usage map[string][]byte
}

// NewMemoryStore returns an empty, ready-to-use in-memory StateStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		projects:       make(map[string]Project),
		tasks:          make(map[string]Task),
		leases:         make(map[string]Lease),
		scenarios:      make(map[string]Scenario),
		hosts:          make(map[string]Host),
		taskDiffs:      make(map[string]TaskDiff),
		creds:          make(map[string]Credential),
		enhance:        make(map[string]EnhanceJob),
		intakeSessions: make(map[string]IntakeSession),
		usage:          make(map[string][]byte),
	}
}

// Compile-time assertion that *MemoryStore satisfies the frozen contract.
var _ StateStore = (*MemoryStore)(nil)

// CreateProject persists a new project, failing with ErrAlreadyExists if one is
// already registered under the same ID.
func (s *MemoryStore) CreateProject(ctx context.Context, p Project) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.ID == "" {
		return fmt.Errorf("create project: %w: empty id", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projects[p.ID]; ok {
		return fmt.Errorf("create project %q: %w", p.ID, ErrAlreadyExists)
	}
	s.projects[p.ID] = cloneProject(p)
	return nil
}

// GetProject returns the project by ID, or a wrapped ErrNotFound.
func (s *MemoryStore) GetProject(ctx context.Context, id string) (Project, error) {
	if err := ctx.Err(); err != nil {
		return Project{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.projects[id]
	if !ok {
		return Project{}, fmt.Errorf("get project %q: %w", id, ErrNotFound)
	}
	return cloneProject(p), nil
}

// ListProjects returns all registered projects ordered by ID.
func (s *MemoryStore) ListProjects(ctx context.Context) ([]Project, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Project, 0, len(s.projects))
	for _, p := range s.projects {
		out = append(out, cloneProject(p))
	}
	slices.SortFunc(out, func(a, b Project) int { return cmpString(a.ID, b.ID) })
	return out, nil
}

// UpdateProject replaces the stored project with the same ID, or returns a
// wrapped ErrNotFound if no project has that ID (ADR-0021 additive mutation).
func (s *MemoryStore) UpdateProject(ctx context.Context, p Project) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.projects[p.ID]; !ok {
		return fmt.Errorf("update project %q: %w", p.ID, ErrNotFound)
	}
	s.projects[p.ID] = cloneProject(p)
	return nil
}

// CreateTask persists a new task, failing with ErrAlreadyExists on a duplicate ID.
func (s *MemoryStore) CreateTask(ctx context.Context, t Task) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.ID == "" {
		return fmt.Errorf("create task: %w: empty id", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[t.ID]; ok {
		return fmt.Errorf("create task %q: %w", t.ID, ErrAlreadyExists)
	}
	s.tasks[t.ID] = cloneTask(t)
	return nil
}

// GetTask returns the task by ID, or a wrapped ErrNotFound.
func (s *MemoryStore) GetTask(ctx context.Context, id string) (Task, error) {
	if err := ctx.Err(); err != nil {
		return Task{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.tasks[id]
	if !ok {
		return Task{}, fmt.Errorf("get task %q: %w", id, ErrNotFound)
	}
	return cloneTask(t), nil
}

// ListTasks returns all tasks for the given project, ordered by task ID.
func (s *MemoryStore) ListTasks(ctx context.Context, projectID string) ([]Task, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Task, 0)
	for _, t := range s.tasks {
		if t.ProjectID == projectID {
			out = append(out, cloneTask(t))
		}
	}
	slices.SortFunc(out, func(a, b Task) int { return cmpString(a.ID, b.ID) })
	return out, nil
}

// UpdateTask persists changes to an existing task, or returns a wrapped
// ErrNotFound if no task has that ID.
func (s *MemoryStore) UpdateTask(ctx context.Context, t Task) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[t.ID]; !ok {
		return fmt.Errorf("update task %q: %w", t.ID, ErrNotFound)
	}
	s.tasks[t.ID] = cloneTask(t)
	return nil
}

// AcquireLease takes the repo-scoped lease atomically, failing with ErrLeaseHeld
// if the project is already leased (single-host Faz-1a semantics).
func (s *MemoryStore) AcquireLease(ctx context.Context, l Lease) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.ProjectID == "" {
		return fmt.Errorf("acquire lease: %w: empty project id", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.leases[l.ProjectID]; ok {
		return fmt.Errorf("acquire lease for project %q: %w", l.ProjectID, ErrLeaseHeld)
	}
	s.leases[l.ProjectID] = l
	return nil
}

// ReleaseLease releases the lease held on the project. It is idempotent:
// releasing a project that holds no lease is not an error.
func (s *MemoryStore) ReleaseLease(ctx context.Context, projectID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.leases, projectID)
	return nil
}

// ReleaseLeaseOwned releases the project's lease ONLY when it is still held by the
// given (hostID, taskID) owner (ADR-0021 additive, C-2). It is idempotent: when no
// lease is held, or the held lease belongs to a DIFFERENT owner (e.g. after a
// false-reap and re-acquire by another host), it deletes nothing and returns nil —
// so a stale holder's release cannot delete the new holder's lease.
func (s *MemoryStore) ReleaseLeaseOwned(ctx context.Context, projectID, hostID, taskID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.leases[projectID]
	if !ok {
		return nil // no lease held → clean no-op.
	}
	if l.HostID != hostID || l.TaskID != taskID {
		return nil // held by a different owner → do NOT delete (C-2).
	}
	delete(s.leases, projectID)
	return nil
}

// GetLease returns the lease on the project, or a wrapped ErrNotFound.
func (s *MemoryStore) GetLease(ctx context.Context, projectID string) (Lease, error) {
	if err := ctx.Err(); err != nil {
		return Lease{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	l, ok := s.leases[projectID]
	if !ok {
		return Lease{}, fmt.Errorf("get lease for project %q: %w", projectID, ErrNotFound)
	}
	return l, nil
}

// ListLeases returns all currently held leases, ordered by project ID.
func (s *MemoryStore) ListLeases(ctx context.Context) ([]Lease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Lease, 0, len(s.leases))
	for _, l := range s.leases {
		out = append(out, l)
	}
	slices.SortFunc(out, func(a, b Lease) int { return cmpString(a.ProjectID, b.ProjectID) })
	return out, nil
}

// RegisterHost upserts the host by ID (ADR-0024 self-registration): a new ID is
// inserted, an existing one has its capabilities and heartbeat overwritten. A
// zero LastHeartbeat is stamped with the current time so a fresh registration is
// always live.
func (s *MemoryStore) RegisterHost(ctx context.Context, h Host) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if h.ID == "" {
		return fmt.Errorf("register host: %w: empty id", ErrInvalid)
	}
	if h.LastHeartbeat.IsZero() {
		h.LastHeartbeat = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hosts[h.ID] = cloneHost(h)
	return nil
}

// HostHeartbeat advances the host's LastHeartbeat to t, or returns a wrapped
// ErrNotFound if the host is not registered.
func (s *MemoryStore) HostHeartbeat(ctx context.Context, hostID string, t time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.hosts[hostID]
	if !ok {
		return fmt.Errorf("host heartbeat %q: %w", hostID, ErrNotFound)
	}
	h.LastHeartbeat = t
	s.hosts[hostID] = h
	return nil
}

// GetHost returns the host by ID, or a wrapped ErrNotFound.
func (s *MemoryStore) GetHost(ctx context.Context, id string) (Host, error) {
	if err := ctx.Err(); err != nil {
		return Host{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.hosts[id]
	if !ok {
		return Host{}, fmt.Errorf("get host %q: %w", id, ErrNotFound)
	}
	return cloneHost(h), nil
}

// ListHosts returns all registered hosts, ordered by ID.
func (s *MemoryStore) ListHosts(ctx context.Context) ([]Host, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Host, 0, len(s.hosts))
	for _, h := range s.hosts {
		out = append(out, cloneHost(h))
	}
	slices.SortFunc(out, func(a, b Host) int { return cmpString(a.ID, b.ID) })
	return out, nil
}

// CreateScenario persists a new scenario, failing with ErrAlreadyExists on a
// duplicate ID.
func (s *MemoryStore) CreateScenario(ctx context.Context, sc Scenario) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if sc.ID == "" {
		return fmt.Errorf("create scenario: %w: empty id", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.scenarios[sc.ID]; ok {
		return fmt.Errorf("create scenario %q: %w", sc.ID, ErrAlreadyExists)
	}
	s.scenarios[sc.ID] = cloneScenario(sc)
	return nil
}

// GetScenario returns the scenario by ID, or a wrapped ErrNotFound.
func (s *MemoryStore) GetScenario(ctx context.Context, id string) (Scenario, error) {
	if err := ctx.Err(); err != nil {
		return Scenario{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	sc, ok := s.scenarios[id]
	if !ok {
		return Scenario{}, fmt.Errorf("get scenario %q: %w", id, ErrNotFound)
	}
	return cloneScenario(sc), nil
}

// AppendScenarioAcceptance appends de-duplicated criteria to a scenario's acceptance list.
func (s *MemoryStore) AppendScenarioAcceptance(ctx context.Context, id string, criteria []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(criteria) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sc, ok := s.scenarios[id]
	if !ok {
		return fmt.Errorf("append scenario acceptance %q: %w", id, ErrNotFound)
	}
	seen := make(map[string]bool, len(sc.Acceptance))
	for _, a := range sc.Acceptance {
		seen[a] = true
	}
	for _, c := range criteria {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		sc.Acceptance = append(sc.Acceptance, c)
	}
	s.scenarios[id] = sc
	return nil
}

// ListScenarios returns all scenarios for the given project, ordered by ID.
func (s *MemoryStore) ListScenarios(ctx context.Context, projectID string) ([]Scenario, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Scenario, 0)
	for _, sc := range s.scenarios {
		if sc.ProjectID == projectID {
			out = append(out, cloneScenario(sc))
		}
	}
	slices.SortFunc(out, func(a, b Scenario) int { return cmpString(a.ID, b.ID) })
	return out, nil
}

// cloneProject returns a value copy of p. Project has only scalar fields, so a
// shallow copy fully isolates it.
func cloneProject(p Project) Project { return p }

// cloneTask returns a deep copy of t, cloning its slice fields so the caller and
// the store never share backing arrays.
func cloneTask(t Task) Task {
	t.Requires = slices.Clone(t.Requires)
	t.Deps = slices.Clone(t.Deps)
	return t
}

// cloneScenario returns a deep copy of sc, cloning its slice fields.
func cloneScenario(sc Scenario) Scenario {
	sc.Deps = slices.Clone(sc.Deps)
	sc.Acceptance = slices.Clone(sc.Acceptance)
	return sc
}

// cloneHost returns a deep copy of h, cloning its capabilities slice so the
// caller and the store never share a backing array.
func cloneHost(h Host) Host {
	h.Capabilities = slices.Clone(h.Capabilities)
	return h
}

// cmpString orders strings ascending for deterministic list ordering.
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
