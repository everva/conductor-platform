package events

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// List-limit bounds for EventReader.ListEvents. A non-positive limit applies
// DefaultListLimit; a limit above MaxListLimit is clamped to MaxListLimit. They
// bound how many historical rows a single read can pull back so a reader (the
// API gateway's GET /events) can't ask for an unbounded replay.
const (
	// DefaultListLimit is applied when ListEvents is called with limit <= 0.
	DefaultListLimit = 200
	// MaxListLimit is the hard ceiling; a larger requested limit is clamped down.
	MaxListLimit = 1000
)

// resolveLimit maps a caller-supplied limit onto the [1, MaxListLimit] range:
// limit <= 0 -> DefaultListLimit, limit > MaxListLimit -> MaxListLimit. It is
// the single place the default/clamp policy lives so PostgresBus and MemoryBus
// agree exactly.
func resolveLimit(limit int) int {
	if limit <= 0 {
		return DefaultListLimit
	}
	if limit > MaxListLimit {
		return MaxListLimit
	}
	return limit
}

// EventReader is the ADDITIVE historical-read seam over the persisted event
// stream (ADR-0021 additive growth; the frozen EventBus is unchanged). It is a
// SEPARATE optional interface a backend MAY implement so a reader (the API
// gateway's GET /events) can replay past events; EventBus stays
// Publish/Subscribe-only.
type EventReader interface {
	// ListEvents returns persisted events matching filter with TS >= since
	// (zero since = from the beginning), in ascending TS order, capped at limit
	// (limit <= 0 applies a sane default; an implementation caps it to a maximum).
	//
	// When more than the resolved limit match, the MOST RECENT limit events are
	// returned (the newest end of the matching window), then ordered ascending for
	// the caller. This is the natural backfill for a live tail: a UI replays the
	// recent history that immediately precedes the live stream rather than the
	// oldest rows ever recorded. (A caller wanting an older slice constrains the
	// window with a smaller `since`.)
	ListEvents(ctx context.Context, filter Filter, since time.Time, limit int) ([]Event, error)
}

// Compile-time assertions that both bus implementations satisfy EventReader.
var (
	_ EventReader = (*PostgresBus)(nil)
	_ EventReader = (*MemoryBus)(nil)
)

// ListEvents reads persisted events from the events table, translating filter
// into parameterized WHERE clauses (never string-concatenating caller input),
// applying the TS >= since cutoff, selecting the MOST RECENT resolved-limit rows
// (ORDER BY ts DESC LIMIT n), then reversing to ascending TS order for the
// caller. It returns ErrBusClosed if the bus is closed. It is the historical-
// replay companion to Publish/Subscribe (ADR-0021 additive seam).
func (b *PostgresBus) ListEvents(ctx context.Context, filter Filter, since time.Time, limit int) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return nil, ErrBusClosed
	}

	// Build the WHERE incrementally with positional params so nothing the caller
	// supplies is ever concatenated into SQL.
	where := make([]string, 0, 6)
	args := make([]any, 0, 6)
	add := func(clause string, val any) {
		args = append(args, val)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}

	if filter.Project != "" {
		add("project = $%d", filter.Project)
	}
	if filter.Task != "" {
		add("task = $%d", filter.Task)
	}
	if filter.Phase != "" {
		add("phase = $%d", string(filter.Phase))
	}
	if filter.Kind != "" {
		add("kind = $%d", string(filter.Kind))
	}
	if filter.InterventionOnly {
		add("kind = $%d", string(KindInterventionNeeded))
	}
	// since: zero time = from the beginning, so only constrain when set.
	add("ts >= $%d", since.UTC())

	limit = resolveLimit(limit)
	args = append(args, limit)

	q := "SELECT id, ts, project, task, phase, kind, payload FROM events"
	for i, c := range where {
		if i == 0 {
			q += " WHERE " + c
		} else {
			q += " AND " + c
		}
	}
	// Select the MOST RECENT rows (DESC LIMIT n); the slice is reversed to
	// ascending below so the caller still receives ascending-by-TS order.
	q += fmt.Sprintf(" ORDER BY ts DESC LIMIT $%d", len(args))

	rows, err := b.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("events: list events: %w", err)
	}
	defer rows.Close()

	out := make([]Event, 0, limit)
	for rows.Next() {
		var (
			ev      Event
			phase   string
			kind    string
			payload []byte
		)
		if err := rows.Scan(&ev.ID, &ev.TS, &ev.Project, &ev.Task, &phase, &kind, &payload); err != nil {
			return nil, fmt.Errorf("events: scan event row: %w", err)
		}
		ev.Phase = Phase(phase)
		ev.Kind = Kind(kind)
		if len(payload) > 0 {
			m := map[string]any{}
			if err := json.Unmarshal(payload, &m); err != nil {
				return nil, fmt.Errorf("events: unmarshal payload for %q: %w", ev.ID, err)
			}
			ev.Payload = m
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("events: iterate event rows: %w", err)
	}
	// Rows came newest-first (DESC); reverse in place to ascending TS order so the
	// caller gets the most-recent window in chronological order.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// ListEvents replays the bounded in-memory retained history: it filters with the
// shared Filter.Matches, applies the TS >= since cutoff, sorts ascending by TS,
// and returns the MOST RECENT resolved-limit events (the newest tail of the
// matching window, still in ascending order) — matching PostgresBus.ListEvents.
// It returns ErrBusClosed if the bus is closed. The returned slice is a fresh
// copy; mutating it never touches the internal history. This is the historical-
// replay companion to Publish/Subscribe (ADR-0021 additive seam).
func (b *MemoryBus) ListEvents(ctx context.Context, filter Filter, since time.Time, limit int) ([]Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cutoff := since.UTC()

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, ErrBusClosed
	}
	matched := make([]Event, 0, len(b.history))
	for _, ev := range b.history {
		if !filter.Matches(ev) {
			continue
		}
		if ev.TS.Before(cutoff) {
			continue
		}
		matched = append(matched, ev)
	}
	b.mu.Unlock()

	// Defensive ascending sort: history is append-order (≈ascending) but a
	// non-monotonic injected clock could violate that, so sort by TS to honor the
	// contract before truncating to the earliest window.
	sort.SliceStable(matched, func(i, j int) bool {
		return matched[i].TS.Before(matched[j].TS)
	})

	limit = resolveLimit(limit)
	if len(matched) > limit {
		// Keep the most-recent `limit` (the tail of the ascending slice), matching
		// PostgresBus's DESC-LIMIT-then-reverse semantics.
		matched = matched[len(matched)-limit:]
	}
	return matched, nil
}
