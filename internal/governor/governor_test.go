package governor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/everva/conductor-platform/internal/statestore"
)

// fakeProbe is a deterministic LoadProbe for the table tests.
type fakeProbe struct {
	load float64
	ok   bool
}

func (f fakeProbe) Load1() (float64, bool) { return f.load, f.ok }

// seedLeases creates a memory store with one lease per supplied project ID.
func seedLeases(t *testing.T, projectIDs ...string) *statestore.MemoryStore {
	t.Helper()
	ctx := context.Background()
	s := statestore.NewMemoryStore()
	for _, pid := range projectIDs {
		if err := s.CreateProject(ctx, statestore.Project{ID: pid}); err != nil {
			t.Fatalf("seed project %q: %v", pid, err)
		}
		if err := s.AcquireLease(ctx, statestore.Lease{ProjectID: pid, HostID: "h", TaskID: "t", AcquiredAt: time.Now()}); err != nil {
			t.Fatalf("seed lease %q: %v", pid, err)
		}
	}
	return s
}

func TestGovernor_Admit_Table(t *testing.T) {
	// Pin NumCPU so normalized-load arithmetic is deterministic regardless of the
	// machine the test runs on.
	orig := numCPU
	numCPU = func() int { return 4 }
	t.Cleanup(func() { numCPU = orig })

	const target = "proj-target"

	tests := []struct {
		name       string
		leases     []string // projects with an active lease
		cfg        Config
		probe      fakeProbe
		wantAdmit  bool
		wantReason Reason
	}{
		{
			name:       "all clear admits",
			leases:     nil,
			cfg:        Config{GlobalCap: 3, LoadCeiling: 2.0},
			probe:      fakeProbe{load: 4.0, ok: true}, // normalized 1.0 < 2.0
			wantAdmit:  true,
			wantReason: ReasonAdmit,
		},
		{
			name:       "repo busy denies (target already leased)",
			leases:     []string{target},
			cfg:        Config{GlobalCap: 10, LoadCeiling: 100},
			probe:      fakeProbe{load: 0, ok: true},
			wantAdmit:  false,
			wantReason: ReasonDenyRepoBusy,
		},
		{
			name:       "global cap at boundary denies",
			leases:     []string{"a", "b", "c"}, // 3 active, cap 3
			cfg:        Config{GlobalCap: 3, LoadCeiling: 100},
			probe:      fakeProbe{load: 0, ok: true},
			wantAdmit:  false,
			wantReason: ReasonDenyGlobalCap,
		},
		{
			name:       "global cap below boundary admits",
			leases:     []string{"a", "b"}, // 2 active, cap 3
			cfg:        Config{GlobalCap: 3, LoadCeiling: 100},
			probe:      fakeProbe{load: 0, ok: true},
			wantAdmit:  true,
			wantReason: ReasonAdmit,
		},
		{
			name:       "host load above ceiling denies",
			leases:     nil,
			cfg:        Config{GlobalCap: 10, LoadCeiling: 1.5},
			probe:      fakeProbe{load: 8.0, ok: true}, // normalized 2.0 > 1.5
			wantAdmit:  false,
			wantReason: ReasonDenyHostLoad,
		},
		{
			name:       "host load exactly at ceiling admits (strict greater-than)",
			leases:     nil,
			cfg:        Config{GlobalCap: 10, LoadCeiling: 2.0},
			probe:      fakeProbe{load: 8.0, ok: true}, // normalized exactly 2.0
			wantAdmit:  true,
			wantReason: ReasonAdmit,
		},
		{
			name:       "unreadable load is permissive",
			leases:     nil,
			cfg:        Config{GlobalCap: 10, LoadCeiling: 0.1},
			probe:      fakeProbe{load: 999, ok: false}, // ok=false => normalized 0
			wantAdmit:  true,
			wantReason: ReasonAdmit,
		},
		{
			name:       "repo-busy wins over global cap when both trip",
			leases:     []string{target, "a", "b"}, // target leased AND cap reached
			cfg:        Config{GlobalCap: 3, LoadCeiling: 100},
			probe:      fakeProbe{load: 0, ok: true},
			wantAdmit:  false,
			wantReason: ReasonDenyRepoBusy,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := seedLeases(t, tc.leases...)
			g, err := New(store, tc.probe, tc.cfg)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			dec, err := g.Admit(context.Background(), target)
			if err != nil {
				t.Fatalf("Admit: %v", err)
			}
			if dec.Admit != tc.wantAdmit {
				t.Fatalf("Admit = %v, want %v (decision=%+v)", dec.Admit, tc.wantAdmit, dec)
			}
			if dec.Reason != tc.wantReason {
				t.Fatalf("Reason = %q, want %q (decision=%+v)", dec.Reason, tc.wantReason, dec)
			}
		})
	}
}

// leaseSpec is a project+host pair for seeding host-attributed leases.
type leaseSpec struct {
	project string
	host    string
}

// seedHostLeases creates a memory store with one lease per spec, attributing
// each lease to its given host so per-host cap counting can be exercised.
func seedHostLeases(t *testing.T, specs ...leaseSpec) *statestore.MemoryStore {
	t.Helper()
	ctx := context.Background()
	s := statestore.NewMemoryStore()
	for _, sp := range specs {
		if err := s.CreateProject(ctx, statestore.Project{ID: sp.project}); err != nil {
			t.Fatalf("seed project %q: %v", sp.project, err)
		}
		if err := s.AcquireLease(ctx, statestore.Lease{ProjectID: sp.project, HostID: sp.host, TaskID: "t", AcquiredAt: time.Now()}); err != nil {
			t.Fatalf("seed lease %q@%q: %v", sp.project, sp.host, err)
		}
	}
	return s
}

func TestGovernor_Admit_HostCap_Table(t *testing.T) {
	// Pin NumCPU so normalized-load arithmetic is deterministic.
	orig := numCPU
	numCPU = func() int { return 4 }
	t.Cleanup(func() { numCPU = orig })

	const target = "proj-target"

	tests := []struct {
		name       string
		leases     []leaseSpec
		cfg        Config
		probe      fakeProbe
		wantAdmit  bool
		wantReason Reason
	}{
		{
			name: "this host at cap denies (host-A 2 active, cap 2)",
			leases: []leaseSpec{
				{project: "a", host: "host-A"},
				{project: "b", host: "host-A"},
			},
			cfg:        Config{GlobalCap: 100, LoadCeiling: 100, HostID: "host-A", HostCap: 2},
			probe:      fakeProbe{load: 0, ok: true},
			wantAdmit:  false,
			wantReason: ReasonDenyHostCap,
		},
		{
			name: "this host below cap admits (host-A 1 active, cap 2)",
			leases: []leaseSpec{
				{project: "a", host: "host-A"},
			},
			cfg:        Config{GlobalCap: 100, LoadCeiling: 100, HostID: "host-A", HostCap: 2},
			probe:      fakeProbe{load: 0, ok: true},
			wantAdmit:  true,
			wantReason: ReasonAdmit,
		},
		{
			name: "other-host leases do not count toward this host's cap",
			leases: []leaseSpec{
				// host-B is full (2 on its own); host-A has none, so host-A's cap of 2
				// is free and admits despite host-B being saturated.
				{project: "a", host: "host-B"},
				{project: "b", host: "host-B"},
			},
			cfg:        Config{GlobalCap: 100, LoadCeiling: 100, HostID: "host-A", HostCap: 2},
			probe:      fakeProbe{load: 0, ok: true},
			wantAdmit:  true,
			wantReason: ReasonAdmit,
		},
		{
			name: "mixed hosts: only this host's leases count (host-A 2 of mixed 3, cap 2)",
			leases: []leaseSpec{
				{project: "a", host: "host-A"},
				{project: "b", host: "host-A"},
				{project: "c", host: "host-B"},
			},
			cfg:        Config{GlobalCap: 100, LoadCeiling: 100, HostID: "host-A", HostCap: 2},
			probe:      fakeProbe{load: 0, ok: true},
			wantAdmit:  false,
			wantReason: ReasonDenyHostCap,
		},
		{
			name: "HostCap=0 never denies on host-cap (host-A many active)",
			leases: []leaseSpec{
				{project: "a", host: "host-A"},
				{project: "b", host: "host-A"},
				{project: "c", host: "host-A"},
			},
			cfg:        Config{GlobalCap: 100, LoadCeiling: 100, HostID: "host-A", HostCap: 0},
			probe:      fakeProbe{load: 0, ok: true},
			wantAdmit:  true,
			wantReason: ReasonAdmit,
		},
		{
			name: "negative HostCap is disabled (treated as unlimited)",
			leases: []leaseSpec{
				{project: "a", host: "host-A"},
				{project: "b", host: "host-A"},
			},
			cfg:        Config{GlobalCap: 100, LoadCeiling: 100, HostID: "host-A", HostCap: -1},
			probe:      fakeProbe{load: 0, ok: true},
			wantAdmit:  true,
			wantReason: ReasonAdmit,
		},
		{
			name: "repo-busy wins over host-cap when both trip",
			leases: []leaseSpec{
				{project: target, host: "host-A"}, // target leased AND host-A at cap
				{project: "b", host: "host-A"},
			},
			cfg:        Config{GlobalCap: 100, LoadCeiling: 100, HostID: "host-A", HostCap: 2},
			probe:      fakeProbe{load: 0, ok: true},
			wantAdmit:  false,
			wantReason: ReasonDenyRepoBusy,
		},
		{
			name: "global cap wins over host-cap when both trip",
			leases: []leaseSpec{
				{project: "a", host: "host-A"},
				{project: "b", host: "host-A"},
			},
			cfg:        Config{GlobalCap: 2, LoadCeiling: 100, HostID: "host-A", HostCap: 2},
			probe:      fakeProbe{load: 0, ok: true},
			wantAdmit:  false,
			wantReason: ReasonDenyGlobalCap,
		},
		{
			name: "host-cap wins over host-load when both trip",
			leases: []leaseSpec{
				{project: "a", host: "host-A"},
				{project: "b", host: "host-A"},
			},
			// host-A at cap AND load over ceiling: documented order puts host-cap first.
			cfg:        Config{GlobalCap: 100, LoadCeiling: 1.0, HostID: "host-A", HostCap: 2},
			probe:      fakeProbe{load: 8.0, ok: true}, // normalized 2.0 > 1.0
			wantAdmit:  false,
			wantReason: ReasonDenyHostCap,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := seedHostLeases(t, tc.leases...)
			g, err := New(store, tc.probe, tc.cfg)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			dec, err := g.Admit(context.Background(), target)
			if err != nil {
				t.Fatalf("Admit: %v", err)
			}
			if dec.Admit != tc.wantAdmit {
				t.Fatalf("Admit = %v, want %v (decision=%+v)", dec.Admit, tc.wantAdmit, dec)
			}
			if dec.Reason != tc.wantReason {
				t.Fatalf("Reason = %q, want %q (decision=%+v)", dec.Reason, tc.wantReason, dec)
			}
		})
	}
}

func TestNew_NilStore_Errors(t *testing.T) {
	if _, err := New(nil, fakeProbe{}, DefaultConfig()); err == nil {
		t.Fatalf("expected error for nil store")
	}
}

func TestNew_DefaultsFillNonPositive(t *testing.T) {
	store := seedLeases(t)
	g, err := New(store, fakeProbe{ok: false}, Config{GlobalCap: 0, LoadCeiling: -1})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if g.cfg.GlobalCap != DefaultGlobalCap {
		t.Fatalf("GlobalCap = %d, want default %d", g.cfg.GlobalCap, DefaultGlobalCap)
	}
	if g.cfg.LoadCeiling != DefaultLoadCeiling {
		t.Fatalf("LoadCeiling = %v, want default %v", g.cfg.LoadCeiling, DefaultLoadCeiling)
	}
}

func TestNew_NilProbe_FallsBackToSystem(t *testing.T) {
	store := seedLeases(t)
	g, err := New(store, nil, DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// A nil probe must not panic on Admit; the system probe is permissive on
	// platforms that cannot read load, so this should not error.
	if _, err := g.Admit(context.Background(), "x"); err != nil {
		t.Fatalf("Admit with system probe: %v", err)
	}
}

// errStore is a StateStore whose ListLeases fails, to prove Admit propagates
// the store error rather than swallowing it.
type errStore struct {
	statestore.StateStore
}

func (errStore) ListLeases(context.Context) ([]statestore.Lease, error) {
	return nil, errors.New("boom")
}

func TestGovernor_Admit_StoreError(t *testing.T) {
	g, err := New(errStore{StateStore: statestore.NewMemoryStore()}, fakeProbe{ok: true}, DefaultConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := g.Admit(context.Background(), "x"); err == nil {
		t.Fatalf("expected error when ListLeases fails")
	}
}

func TestSystemLoadProbe_DoesNotPanic(t *testing.T) {
	// Smoke test the real probe on whatever platform runs CI: it must return
	// without panicking. The value is not asserted (it is host-dependent); only
	// that ok+value are coherent.
	load, ok := SystemLoadProbe{}.Load1()
	if ok && load < 0 {
		t.Fatalf("ok probe returned negative load %v", load)
	}
}
