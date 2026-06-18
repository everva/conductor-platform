package statestore

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestStateStoreIsImplementable is a compile-time guard that the frozen
// StateStore signature set can be satisfied. Behaviour (in-memory, then
// Postgres) lands in later tasks (ADR-0013); this only pins the surface.
func TestStateStoreIsImplementable(t *testing.T) {
	var _ StateStore = (*stubStore)(nil)
}

// TestErrNotFoundIsSentinel confirms ErrNotFound is a comparable sentinel so
// callers can use errors.Is — ADR-0014 resilience maps onto these names.
func TestErrNotFoundIsSentinel(t *testing.T) {
	wrapped := errors.Join(errors.New("lookup project p1"), ErrNotFound)
	if !errors.Is(wrapped, ErrNotFound) {
		t.Fatal("ErrNotFound must be detectable via errors.Is when wrapped")
	}
}

type stubStore struct{}

func (*stubStore) CreateProject(context.Context, Project) error           { return nil }
func (*stubStore) GetProject(context.Context, string) (Project, error)    { return Project{}, nil }
func (*stubStore) ListProjects(context.Context) ([]Project, error)        { return nil, nil }
func (*stubStore) UpdateProject(context.Context, Project) error           { return nil }
func (*stubStore) CreateTask(context.Context, Task) error                 { return nil }
func (*stubStore) GetTask(context.Context, string) (Task, error)          { return Task{}, nil }
func (*stubStore) ListTasks(context.Context, string) ([]Task, error)      { return nil, nil }
func (*stubStore) UpdateTask(context.Context, Task) error                 { return nil }
func (*stubStore) AcquireLease(context.Context, Lease) error              { return nil }
func (*stubStore) ReleaseLease(context.Context, string) error             { return nil }
func (*stubStore) GetLease(context.Context, string) (Lease, error)        { return Lease{}, nil }
func (*stubStore) ListLeases(context.Context) ([]Lease, error)            { return nil, nil }
func (*stubStore) RegisterHost(context.Context, Host) error               { return nil }
func (*stubStore) HostHeartbeat(context.Context, string, time.Time) error { return nil }
func (*stubStore) GetHost(context.Context, string) (Host, error)          { return Host{}, nil }
func (*stubStore) ListHosts(context.Context) ([]Host, error)              { return nil, nil }
func (*stubStore) CreateScenario(context.Context, Scenario) error         { return nil }
func (*stubStore) GetScenario(context.Context, string) (Scenario, error)  { return Scenario{}, nil }
func (*stubStore) ListScenarios(context.Context, string) ([]Scenario, error) {
	return nil, nil
}
