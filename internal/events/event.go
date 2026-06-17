// Package events is the conductor platform's event / observability system
// (ADR-0011): the single Go source of truth for the live event taxonomy, an
// EventBus abstraction with an in-memory implementation, and a Postgres
// LISTEN/NOTIFY implementation that persists events and pushes them to
// subscribers across processes/hosts in realtime.
//
// The taxonomy here is the FROZEN one from ADR-0011. The engine's
// engine.Event (ADR-0002) is the normalized envelope the engine emits from its
// subprocess output; this package's Event is the platform-owned envelope that
// gets persisted + NOTIFY-ed. They carry the same phase/kind vocabulary so an
// engine.Event maps onto an events.Event without translation drift (see
// FromEngine). The platform owns persistence and NOTIFY; the engine never
// touches Postgres (ADR-0011 §Güncelleme).
//
// This Go definition is the single source: the TS type for the event taxonomy
// is generated from it (see cmd/eventgen) so client and server cannot drift.
package events

import (
	"encoding/json"
	"fmt"
	"time"
)

// Phase is a task-lifecycle phase an event belongs to. The set is frozen by
// ADR-0011: plan, develop, test, review, verify, merge. The conductor tick
// maps onto these (intake→plan; Develop→develop/test; Verify→review/verify;
// conductor merge→merge).
type Phase string

// The frozen phase vocabulary (ADR-0011 §1).
const (
	// PhasePlan is intake / scenario planning (intake→plan).
	PhasePlan Phase = "plan"
	// PhaseDevelop is the in-performer development pipeline (Develop verb).
	PhaseDevelop Phase = "develop"
	// PhaseTest is in-performer testing within develop.
	PhaseTest Phase = "test"
	// PhaseReview is the independent fresh-eyes review (Verify verb).
	PhaseReview Phase = "review"
	// PhaseVerify is the deterministic gate + hidden-holdout verification.
	PhaseVerify Phase = "verify"
	// PhaseMerge is the conductor squash-merge into the base branch.
	PhaseMerge Phase = "merge"
)

// Phases is the canonical, ordered list of valid phases. It is the single list
// the validator, the TS generator, and any UI enumerate from.
var Phases = []Phase{
	PhasePlan,
	PhaseDevelop,
	PhaseTest,
	PhaseReview,
	PhaseVerify,
	PhaseMerge,
}

// Valid reports whether p is one of the frozen phases.
func (p Phase) Valid() bool {
	for _, v := range Phases {
		if p == v {
			return true
		}
	}
	return false
}

// Kind is the kind of an event. The set is frozen by ADR-0011: started,
// progress, log, diff, decision, health, pr, merge, intervention-needed.
// intervention-needed is the human-gate signal (T3/T4, blocked, readiness —
// ADR-0003/0009); a UI lights up "müdahale gerek" on it.
type Kind string

// The frozen kind vocabulary (ADR-0011 §1).
const (
	// KindStarted marks a phase beginning.
	KindStarted Kind = "started"
	// KindProgress is incremental progress within a phase.
	KindProgress Kind = "progress"
	// KindLog is a normalized log line from a performer/subprocess.
	KindLog Kind = "log"
	// KindDiff is a code-change/diff event.
	KindDiff Kind = "diff"
	// KindDecision is a verdict/decision (pass/fail/changes-requested, etc.).
	KindDecision Kind = "decision"
	// KindHealth is a deterministic liveness signal (sentinel Layer-1, ADR-0006).
	KindHealth Kind = "health"
	// KindPR is a pull-request lifecycle event.
	KindPR Kind = "pr"
	// KindMerge is a merge lifecycle event.
	KindMerge Kind = "merge"
	// KindInterventionNeeded is the human-gate signal: a human must intervene
	// (ADR-0003/0009). It is the canonical machine-readable intervention marker;
	// Event.InterventionNeeded reports it without string-matching at call sites.
	KindInterventionNeeded Kind = "intervention-needed"
)

// Kinds is the canonical, ordered list of valid kinds. It is the single list
// the validator, the TS generator, and any UI enumerate from.
var Kinds = []Kind{
	KindStarted,
	KindProgress,
	KindLog,
	KindDiff,
	KindDecision,
	KindHealth,
	KindPR,
	KindMerge,
	KindInterventionNeeded,
}

// Valid reports whether k is one of the frozen kinds.
func (k Kind) Valid() bool {
	for _, v := range Kinds {
		if k == v {
			return true
		}
	}
	return false
}

// Event is the platform-owned observability envelope (ADR-0011 §1): the unit
// persisted to Postgres and pushed to subscribers via LISTEN/NOTIFY. Its JSON
// shape is the single source the TS type is generated from, so the field tags
// here are load-bearing.
type Event struct {
	// ID is a stable, unique identifier for the event. The EventBus assigns one
	// when empty so persisted rows and NOTIFY payloads always carry an ID.
	ID string `json:"id"`
	// TS is when the event occurred. The EventBus stamps it (UTC) when zero.
	TS time.Time `json:"ts"`
	// Project is the project the event belongs to.
	Project string `json:"project"`
	// Task is the task the event belongs to.
	Task string `json:"task"`
	// Phase is the lifecycle phase (one of Phases).
	Phase Phase `json:"phase"`
	// Kind is the event kind (one of Kinds).
	Kind Kind `json:"kind"`
	// Payload carries kind-specific structured data; nil is allowed and is
	// normalized to an empty object on the wire by MarshalJSON.
	Payload map[string]any `json:"payload"`
}

// InterventionNeeded reports whether this event is the human-gate signal — i.e.
// its Kind is KindInterventionNeeded. Call sites use this rather than comparing
// the kind string so the signal has one definition (ADR-0011 §1).
func (e Event) InterventionNeeded() bool {
	return e.Kind == KindInterventionNeeded
}

// Validate checks the event carries a known phase and kind and a project. It
// does NOT require an ID or TS — the bus fills those — so callers can publish a
// minimally-populated event. An invalid phase/kind is a programming error
// surfaced loudly rather than persisted as garbage.
func (e Event) Validate() error {
	if e.Project == "" {
		return fmt.Errorf("%w: empty project", ErrInvalidEvent)
	}
	if !e.Phase.Valid() {
		return fmt.Errorf("%w: unknown phase %q", ErrInvalidEvent, e.Phase)
	}
	if !e.Kind.Valid() {
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidEvent, e.Kind)
	}
	return nil
}

// MarshalJSON encodes the event, normalizing a nil Payload to an empty object
// so the on-wire/at-rest shape is stable for consumers (TS, UI). It uses an
// alias type to avoid recursing into Event.MarshalJSON.
func (e Event) MarshalJSON() ([]byte, error) {
	type alias Event
	a := alias(e)
	if a.Payload == nil {
		a.Payload = map[string]any{}
	}
	b, err := json.Marshal(a)
	if err != nil {
		return nil, fmt.Errorf("events: marshal event: %w", err)
	}
	return b, nil
}
