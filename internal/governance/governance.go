// Package governance is the risk-layered merge policy (ADR-0003): a deterministic
// gate-PASS is necessary but not always sufficient to auto-merge. High-risk tiers
// must require a HUMAN approval before the merge lands, even when the gate is
// green.
//
// ADR-0003 §Risk-katmanı draws the line by risk tier:
//
//   - Low risk (T1/T2 — normal feature/UI) → local gate is enough, CI is a
//     post-merge safety net. The conductor may auto-merge on an independent PASS.
//   - High risk (T3/T4 — auth, migration, money/payment, RBAC) → a human approval
//     is required in addition to the green gate; the conductor must HOLD the task
//     for a human rather than auto-merge it.
//
// The Policy here is small, deterministic, and side-effect free: MergeMode reads a
// task's Tier and returns a Decision (AutoMerge vs HumanRequired) with a structured
// reason. It mutates nothing and takes no I/O — the conductor consults it BETWEEN
// "verify passed" and "merge", mirroring the optional-seam style of the governor
// (N-5) and emitter (N-9).
//
// The tier→mode mapping is data-driven (a map), so a project can be configured with
// a different policy without changing code. The default mapping is the ADR's
// canonical one; an unknown/empty tier fails SAFE to HumanRequired (see New).
package governance

import (
	"github.com/everva/conductor-platform/internal/statestore"
)

// MergeMode is the policy's verdict for a task: may the conductor auto-merge it on
// a green gate, or must it hold the task for a human first?
type MergeMode string

const (
	// ModeAutoMerge means a green independent gate is sufficient: the conductor may
	// squash-merge the task automatically (low-risk tiers, ADR-0003).
	ModeAutoMerge MergeMode = "auto-merge"
	// ModeHumanRequired means a green gate is necessary but NOT sufficient: a human
	// must approve before the merge lands, so the conductor holds the task as
	// awaiting-human rather than merging it (high-risk tiers, ADR-0003).
	ModeHumanRequired MergeMode = "human-required"
)

// Reason is the structured cause of a merge-mode decision, so a caller can log /
// surface the cause without parsing free text.
type Reason string

const (
	// ReasonLowTier means the task's tier is configured for auto-merge.
	ReasonLowTier Reason = "low-tier-auto-merge"
	// ReasonHighTier means the task's tier is configured to require a human.
	ReasonHighTier Reason = "high-tier-human-required"
	// ReasonUnknownTier means the tier is empty or not in the policy map, so the
	// fail-safe default (HumanRequired) was applied.
	ReasonUnknownTier Reason = "unknown-tier-fail-safe-human-required"
)

// Decision is the result of MergeMode: the chosen mode and the structured reason.
type Decision struct {
	// Mode is the chosen merge mode (auto-merge or human-required).
	Mode MergeMode
	// Reason is the structured cause of the decision.
	Reason Reason
	// Tier is the task tier the decision keyed on (echoed for logging).
	Tier string
}

// HumanRequired reports whether this decision holds the task for a human. Callers
// use this rather than comparing Mode at the call site so the gate has one
// definition.
func (d Decision) HumanRequired() bool { return d.Mode == ModeHumanRequired }

// The canonical tier values (ADR-0003 T1..T4, mirrored from intake's closed set).
// They are duplicated here as plain strings rather than imported so governance
// stays decoupled from intake's parsing; the values are frozen by the ADR.
const (
	TierT1 = "T1"
	TierT2 = "T2"
	TierT3 = "T3"
	TierT4 = "T4"
)

// DefaultMapping is the ADR-0003 canonical tier→mode mapping: low tiers (T1/T2)
// auto-merge on a green gate; high tiers (T3/T4) require a human approval even when
// the gate is green. It is returned as a fresh copy by DefaultPolicy so callers
// cannot mutate a shared map.
func defaultMapping() map[string]MergeMode {
	return map[string]MergeMode{
		TierT1: ModeAutoMerge,
		TierT2: ModeAutoMerge,
		TierT3: ModeHumanRequired,
		TierT4: ModeHumanRequired,
	}
}

// Policy decides the merge mode for a task from its risk tier (ADR-0003). It is
// data-driven: the tier→mode mapping is configurable, and any tier not in the
// mapping (including the empty tier) falls back to a single fail-safe default.
//
// Construct a Policy with New (custom mapping) or DefaultPolicy (the ADR mapping).
// The zero Policy is not usable — its nil mapping would map every tier to the
// default — so always construct via the helpers.
type Policy struct {
	// mapping is the tier→mode table. A tier absent from it uses fallback.
	mapping map[string]MergeMode
	// fallback is applied to any tier not present in mapping (empty/unknown).
	fallback MergeMode
}

// New returns a Policy over the given tier→mode mapping. The mapping is copied so
// later mutation of the caller's map cannot change the policy. A nil/empty mapping
// is allowed (every tier then takes the fail-safe fallback).
//
// The fail-safe default is HumanRequired: an unknown or empty tier is treated as
// high-risk and HELD for a human rather than silently auto-merged. This is the
// conservative choice the ADR's "yeşil değilse merge yok" spirit demands — a
// mis-tagged or untiered task must never auto-merge by accident; a human decides.
func New(mapping map[string]MergeMode) *Policy {
	m := make(map[string]MergeMode, len(mapping))
	for tier, mode := range mapping {
		m[tier] = mode
	}
	return &Policy{mapping: m, fallback: ModeHumanRequired}
}

// DefaultPolicy returns a Policy with the ADR-0003 canonical mapping
// (T1/T2 → auto-merge, T3/T4 → human-required) and the HumanRequired fail-safe.
func DefaultPolicy() *Policy {
	return New(defaultMapping())
}

// MergeMode returns the merge-mode Decision for the task, keyed on its Tier. It is
// deterministic and side-effect free. An unknown/empty tier yields the fail-safe
// fallback (HumanRequired) with ReasonUnknownTier so the cause is explicit.
func (p *Policy) MergeMode(task statestore.Task) Decision {
	tier := task.Tier
	mode, known := p.mapping[tier]
	if !known {
		return Decision{Mode: p.fallback, Reason: ReasonUnknownTier, Tier: tier}
	}
	reason := ReasonLowTier
	if mode == ModeHumanRequired {
		reason = ReasonHighTier
	}
	return Decision{Mode: mode, Reason: reason, Tier: tier}
}
