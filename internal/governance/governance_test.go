package governance

import (
	"testing"

	"github.com/everva/conductor-platform/internal/statestore"
)

// TestDefaultPolicy_MergeMode is the deterministic table for the ADR-0003 default
// mapping: every known tier → expected mode, plus the unknown/empty fail-safe.
func TestDefaultPolicy_MergeMode(t *testing.T) {
	p := DefaultPolicy()

	cases := []struct {
		name       string
		tier       string
		wantMode   MergeMode
		wantReason Reason
		wantHuman  bool
	}{
		{"T1 auto-merge", "T1", ModeAutoMerge, ReasonLowTier, false},
		{"T2 auto-merge", "T2", ModeAutoMerge, ReasonLowTier, false},
		{"T3 human-required", "T3", ModeHumanRequired, ReasonHighTier, true},
		{"T4 human-required", "T4", ModeHumanRequired, ReasonHighTier, true},
		{"empty tier fail-safe", "", ModeHumanRequired, ReasonUnknownTier, true},
		{"unknown tier fail-safe", "T9", ModeHumanRequired, ReasonUnknownTier, true},
		{"garbage tier fail-safe", "not-a-tier", ModeHumanRequired, ReasonUnknownTier, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := p.MergeMode(statestore.Task{ID: "T-1", Tier: tc.tier})
			if got.Mode != tc.wantMode {
				t.Fatalf("Mode = %q, want %q", got.Mode, tc.wantMode)
			}
			if got.Reason != tc.wantReason {
				t.Fatalf("Reason = %q, want %q", got.Reason, tc.wantReason)
			}
			if got.HumanRequired() != tc.wantHuman {
				t.Fatalf("HumanRequired() = %v, want %v", got.HumanRequired(), tc.wantHuman)
			}
			if got.Tier != tc.tier {
				t.Fatalf("Tier = %q, want %q", got.Tier, tc.tier)
			}
		})
	}
}

// TestNew_CustomMapping proves the mapping is data-driven: a project can flip a
// tier to a different mode, and tiers absent from the custom map still fail safe.
func TestNew_CustomMapping(t *testing.T) {
	// A stricter policy where even T2 requires a human; T1 still auto-merges.
	p := New(map[string]MergeMode{
		"T1": ModeAutoMerge,
		"T2": ModeHumanRequired,
	})

	if got := p.MergeMode(statestore.Task{Tier: "T1"}); got.Mode != ModeAutoMerge {
		t.Fatalf("T1 Mode = %q, want auto-merge", got.Mode)
	}
	if got := p.MergeMode(statestore.Task{Tier: "T2"}); got.Mode != ModeHumanRequired {
		t.Fatalf("T2 Mode = %q, want human-required", got.Mode)
	}
	// T3 is absent from the custom map → fail-safe human-required.
	got := p.MergeMode(statestore.Task{Tier: "T3"})
	if got.Mode != ModeHumanRequired || got.Reason != ReasonUnknownTier {
		t.Fatalf("absent T3 = %+v, want human-required/unknown-tier fail-safe", got)
	}
}

// TestNew_MappingCopied proves New copies the caller's map so later mutation can't
// retroactively change policy decisions.
func TestNew_MappingCopied(t *testing.T) {
	src := map[string]MergeMode{"T1": ModeAutoMerge}
	p := New(src)
	src["T1"] = ModeHumanRequired // mutate after construction

	if got := p.MergeMode(statestore.Task{Tier: "T1"}); got.Mode != ModeAutoMerge {
		t.Fatalf("policy changed after caller mutated source map: Mode = %q, want auto-merge", got.Mode)
	}
}

// TestNew_NilMapping proves a nil mapping is usable: every tier fails safe.
func TestNew_NilMapping(t *testing.T) {
	p := New(nil)
	if got := p.MergeMode(statestore.Task{Tier: "T1"}); got.Mode != ModeHumanRequired {
		t.Fatalf("nil-mapping Mode = %q, want fail-safe human-required", got.Mode)
	}
}
