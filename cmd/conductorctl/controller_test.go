package main

import (
	"context"
	"testing"

	"github.com/everva/conductor-platform/internal/conductor"
	"github.com/everva/conductor-platform/internal/statestore"
)

// TestStoreController_PersistsThroughSharedStore proves the operator-side control
// (conductorctl) writes pause DURABLY into the shared store, so a separate reader
// — the daemon's conductor.StorePauser over the SAME store — honors it. This is
// the cross-process reverse-channel the in-memory MemoryController cannot provide.
func TestStoreController_PersistsThroughSharedStore(t *testing.T) {
	ctx := context.Background()
	store := statestore.NewMemoryStore()
	ctrl := NewStoreController(store)

	if paused, err := ctrl.Paused(ctx, "repo"); err != nil || paused {
		t.Fatalf("fresh project must be unpaused: paused=%v err=%v", paused, err)
	}
	if err := ctrl.Pause(ctx, "repo"); err != nil {
		t.Fatalf("pause: %v", err)
	}

	// The daemon's pause-gate reads the SAME store via a fresh StorePauser.
	daemonView := conductor.NewStorePauser(store)
	if paused, err := daemonView.Paused(ctx, "repo"); err != nil || !paused {
		t.Fatalf("daemon view must see persisted pause: paused=%v err=%v", paused, err)
	}

	if err := ctrl.Resume(ctx, "repo"); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if paused, err := daemonView.Paused(ctx, "repo"); err != nil || paused {
		t.Fatalf("daemon view must see resume: paused=%v err=%v", paused, err)
	}
}
