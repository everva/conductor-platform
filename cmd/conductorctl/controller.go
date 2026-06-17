package main

import (
	"context"
	"sync"
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
