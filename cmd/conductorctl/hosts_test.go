package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/everva/conductor-platform/internal/statestore"
)

// TestConductorctl_Hosts_ListsRegisteredHosts is the VISIBLE contract for the
// `hosts` subcommand (HOSTS-1): it lists every registered host from the SAME
// StateStore the daemon uses, showing each host's ID and capabilities. The 2B-1
// host registry added RegisterHost + ListHosts; this is the operator-facing view
// of it, mirroring the existing status/abort/approve handlers' style.
//
// Contract:
//   - a.hosts(ctx, false) renders a human table over the shared store with no error.
//   - The output lists BOTH registered host IDs.
//   - The output shows each host's declared capabilities.
func TestConductorctl_Hosts_ListsRegisteredHosts(t *testing.T) {
	ctx := context.Background()
	a, store, _, out := newApp(t)

	if err := store.RegisterHost(ctx, statestore.Host{
		ID:            "host-alpha",
		Capabilities:  []string{"linux", "backend"},
		LastHeartbeat: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("register host-alpha: %v", err)
	}
	if err := store.RegisterHost(ctx, statestore.Host{
		ID:            "host-beta",
		Capabilities:  []string{"macos", "ios-build"},
		LastHeartbeat: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("register host-beta: %v", err)
	}

	if err := a.hosts(ctx, false); err != nil {
		t.Fatalf("hosts: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"host-alpha", "host-beta", // both IDs listed
		"linux", "backend", // alpha capabilities
		"macos", "ios-build", // beta capabilities
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("hosts output missing %q\n--- output ---\n%s", want, got)
		}
	}
}
