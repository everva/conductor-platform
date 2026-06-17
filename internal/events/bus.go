package events

import (
	"context"
	"errors"

	"github.com/everva/conductor-platform/internal/engine"
)

// Sentinel errors for the event system. Callers detect them with errors.Is.
var (
	// ErrInvalidEvent means an event failed Validate (unknown phase/kind, or an
	// empty project) — a programming error, surfaced rather than persisted.
	ErrInvalidEvent = errors.New("events: invalid event")
	// ErrBusClosed means Publish/Subscribe was called on a closed bus.
	ErrBusClosed = errors.New("events: bus closed")
)

// Filter narrows a subscription to events of interest. A zero Filter (all
// fields empty) matches everything. Set fields are ANDed together;
// InterventionOnly additionally requires KindInterventionNeeded.
type Filter struct {
	// Project, when non-empty, matches only events for that project.
	Project string
	// Task, when non-empty, matches only events for that task.
	Task string
	// Phase, when non-empty, matches only events in that phase.
	Phase Phase
	// Kind, when non-empty, matches only events of that kind.
	Kind Kind
	// InterventionOnly, when true, matches only intervention-needed events
	// (the human-gate stream a UI subscribes to).
	InterventionOnly bool
}

// Matches reports whether ev satisfies the filter. A zero Filter matches all.
func (f Filter) Matches(ev Event) bool {
	if f.Project != "" && ev.Project != f.Project {
		return false
	}
	if f.Task != "" && ev.Task != f.Task {
		return false
	}
	if f.Phase != "" && ev.Phase != f.Phase {
		return false
	}
	if f.Kind != "" && ev.Kind != f.Kind {
		return false
	}
	if f.InterventionOnly && !ev.InterventionNeeded() {
		return false
	}
	return true
}

// CancelFunc unsubscribes a subscription and releases its resources. It is
// idempotent: calling it more than once is a no-op.
type CancelFunc func()

// EventBus is the publish/subscribe seam over the event stream (ADR-0011). An
// implementation persists/transports events however it likes (in-memory for
// tests and single-process; Postgres LISTEN/NOTIFY for cross-host realtime) but
// presents the same contract: Publish fans an event out to every matching live
// subscriber; Subscribe returns a receive-only channel plus a cancel.
//
// The returned channel is closed when the subscription is cancelled (via the
// CancelFunc, via ctx cancellation, or when the bus closes). Subscribers must
// drain promptly; an implementation MAY drop events to a slow subscriber rather
// than block all publishers, but MUST NOT silently corrupt other subscribers.
type EventBus interface {
	// Publish validates and emits ev to all matching subscribers. It fills ev.ID
	// and ev.TS when empty. It returns ErrInvalidEvent for a bad event and
	// ErrBusClosed if the bus is closed.
	Publish(ctx context.Context, ev Event) error
	// Subscribe registers a subscription matching filter and returns its event
	// channel and a CancelFunc. The channel is closed on cancel/ctx-done/close.
	Subscribe(ctx context.Context, filter Filter) (<-chan Event, CancelFunc, error)
}

// FromEngine maps a normalized engine.Event (ADR-0002) onto a platform
// events.Event. The two share the ADR-0011 phase/kind vocabulary, so the map is
// a direct field copy with no translation — keeping the engine and platform
// envelopes from drifting. ID and TS are left for the bus to fill when zero.
func FromEngine(ev engine.Event) Event {
	return Event{
		TS:      ev.TS,
		Project: ev.Project,
		Task:    ev.Task,
		Phase:   Phase(ev.Phase),
		Kind:    Kind(ev.Kind),
		Payload: ev.Payload,
	}
}
