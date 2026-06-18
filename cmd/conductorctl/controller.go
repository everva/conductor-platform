package main

import (
	"context"
	"sync"

	"github.com/everva/conductor-platform/internal/conductor"
	"github.com/everva/conductor-platform/internal/statestore"
)

// Controller is the run-state control seam the CLI toggles and the conductor loop
// reads before each tick (C-2 pause/resume). It is an INJECTED interface so Faz-1b
// can swap the Faz-1a in-process/file flag for the Postgres command table
// (ADR-0011/0010 §O5) without touching the command handlers.
//
// Paused reports whether the project's loop is currently paused; a paused project
// makes the next Tick a no-op. Pause/Resume are idempotent: pausing an already
// paused project (or resuming a running one) is a no-op, never a flip.
type Controller interface {
	// Paused reports whether the project's conductor loop is currently paused.
	Paused(ctx context.Context, projectID string) (bool, error)
	// Pause marks the project paused so the next Tick is a no-op. Idempotent.
	Pause(ctx context.Context, projectID string) error
	// Resume clears the paused flag so ticks run again. Idempotent.
	Resume(ctx context.Context, projectID string) error
}

// MemoryController is the Faz-1a in-process Controller: a concurrency-safe set of
// paused project IDs. It is the swappable default; Faz-1b replaces it with a
// Postgres-backed controller behind the same interface. Construct with
// NewMemoryController.
type MemoryController struct {
	mu     sync.RWMutex
	paused map[string]bool
}

// NewMemoryController returns an empty, ready-to-use in-process Controller.
func NewMemoryController() *MemoryController {
	return &MemoryController{paused: make(map[string]bool)}
}

// Compile-time assertion that *MemoryController satisfies the seam.
var _ Controller = (*MemoryController)(nil)

// Paused reports whether projectID is paused.
func (c *MemoryController) Paused(_ context.Context, projectID string) (bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.paused[projectID], nil
}

// Pause marks projectID paused. Idempotent: a double-pause stays paused.
func (c *MemoryController) Pause(_ context.Context, projectID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.paused[projectID] = true
	return nil
}

// Resume clears the paused flag for projectID. Idempotent: resuming a running
// project is a no-op.
func (c *MemoryController) Resume(_ context.Context, projectID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.paused, projectID)
	return nil
}

// StoreController is the DURABLE, cross-process Controller: it persists the pause
// state THROUGH the shared StateStore rather than an in-process map, so a
// SEPARATE daemon process reading the SAME store honors it (ADR-0011 §4). It
// delegates to conductor.StorePauser — the ONE place that owns the durable pause
// representation (the project's first-class Paused run-state, ADR-0021) — so
// conductorctl (operator side) and the daemon (pause-gate side) agree on the
// shape. When pointed at the shared Postgres store (-dsn),
// `conductorctl pause` makes the daemon's next tick a clean no-op; `resume`
// restores it.
type StoreController struct {
	pauser *conductor.StorePauser
}

// NewStoreController returns a Controller that persists pause state through the
// given shared StateStore so a separate daemon process honors it.
func NewStoreController(store statestore.StateStore) *StoreController {
	return &StoreController{pauser: conductor.NewStorePauser(store)}
}

// Compile-time assertion that *StoreController satisfies the seam.
var _ Controller = (*StoreController)(nil)

// Paused reports whether projectID is paused by reading the project's run-state
// off the shared store (never a cached flag).
func (c *StoreController) Paused(ctx context.Context, projectID string) (bool, error) {
	return c.pauser.Paused(ctx, projectID)
}

// Pause persists the pause directive onto the project's run-state through the
// shared store. Idempotent.
func (c *StoreController) Pause(ctx context.Context, projectID string) error {
	return c.pauser.Pause(ctx, projectID)
}

// Resume persists the resume directive, clearing the project's pause run-state so
// ticks proceed. Idempotent.
func (c *StoreController) Resume(ctx context.Context, projectID string) error {
	return c.pauser.Resume(ctx, projectID)
}
