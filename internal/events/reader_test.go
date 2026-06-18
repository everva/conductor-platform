package events

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

// seqClock returns a clock func that hands out monotonically increasing
// timestamps one second apart starting at base, so published events get
// deterministic, ordered TS values without a real wall clock.
func seqClock(base time.Time) func() time.Time {
	n := -1
	return func() time.Time {
		n++
		return base.Add(time.Duration(n) * time.Second)
	}
}

// seqID returns an ID func handing out id-0, id-1, ... so events are
// individually identifiable in deterministic tests.
func seqID() func() string {
	n := -1
	return func() string {
		n++
		return fmt.Sprintf("id-%d", n)
	}
}

// memReaderBus builds a MemoryBus with deterministic sequenced clock+IDs.
func memReaderBus(base time.Time, opts ...MemoryOption) *MemoryBus {
	o := []MemoryOption{WithClock(seqClock(base)), WithIDFunc(seqID())}
	return NewMemoryBus(append(o, opts...)...)
}

func mustPublish(t *testing.T, b *MemoryBus, evs ...Event) {
	t.Helper()
	ctx := context.Background()
	for i, ev := range evs {
		if err := b.Publish(ctx, ev); err != nil {
			t.Fatalf("publish[%d]: %v", i, err)
		}
	}
}

func tsList(evs []Event) []time.Time {
	out := make([]time.Time, len(evs))
	for i, e := range evs {
		out[i] = e.TS
	}
	return out
}

func TestMemoryListEventsAscendingAllZeroFilter(t *testing.T) {
	base := time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)
	b := memReaderBus(base)
	mustPublish(t,
		b,
		Event{Project: "p1", Task: "T1", Phase: PhasePlan, Kind: KindStarted},
		Event{Project: "p2", Task: "T2", Phase: PhaseDevelop, Kind: KindLog},
		Event{Project: "p1", Task: "T3", Phase: PhaseVerify, Kind: KindInterventionNeeded},
	)

	got, err := b.ListEvents(context.Background(), Filter{}, time.Time{}, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3, got %d", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].TS.Before(got[i-1].TS) {
			t.Fatalf("not ascending at %d: %v", i, tsList(got))
		}
	}
	if got[0].ID != "id-0" || got[2].ID != "id-2" {
		t.Fatalf("unexpected order: %v", got)
	}
}

func TestMemoryListEventsFilters(t *testing.T) {
	base := time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)
	b := memReaderBus(base)
	mustPublish(t,
		b,
		Event{Project: "p1", Task: "T1", Phase: PhasePlan, Kind: KindStarted},
		Event{Project: "p2", Task: "T2", Phase: PhaseDevelop, Kind: KindLog},
		Event{Project: "p1", Task: "T2", Phase: PhaseVerify, Kind: KindInterventionNeeded},
		Event{Project: "p2", Task: "T1", Phase: PhaseReview, Kind: KindDecision},
	)
	ctx := context.Background()

	t.Run("byProject", func(t *testing.T) {
		got, err := b.ListEvents(ctx, Filter{Project: "p1"}, time.Time{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 p1 events, got %d", len(got))
		}
		for _, e := range got {
			if e.Project != "p1" {
				t.Fatalf("wrong project: %+v", e)
			}
		}
	})

	t.Run("byTask", func(t *testing.T) {
		got, err := b.ListEvents(ctx, Filter{Task: "T2"}, time.Time{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 T2 events, got %d", len(got))
		}
	})

	t.Run("byKind", func(t *testing.T) {
		got, err := b.ListEvents(ctx, Filter{Kind: KindLog}, time.Time{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].Kind != KindLog {
			t.Fatalf("expected 1 log event, got %+v", got)
		}
	})

	t.Run("interventionOnly", func(t *testing.T) {
		got, err := b.ListEvents(ctx, Filter{InterventionOnly: true}, time.Time{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || !got[0].InterventionNeeded() {
			t.Fatalf("expected 1 intervention event, got %+v", got)
		}
	})
}

func TestMemoryListEventsSinceCutoff(t *testing.T) {
	base := time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)
	b := memReaderBus(base)
	// Published at base+0s, +1s, +2s, +3s.
	mustPublish(t,
		b,
		Event{Project: "p", Phase: PhasePlan, Kind: KindStarted},
		Event{Project: "p", Phase: PhaseDevelop, Kind: KindLog},
		Event{Project: "p", Phase: PhaseVerify, Kind: KindDecision},
		Event{Project: "p", Phase: PhaseMerge, Kind: KindMerge},
	)
	// since = base+2s should exclude the first two (base+0s, base+1s).
	got, err := b.ListEvents(context.Background(), Filter{}, base.Add(2*time.Second), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 after cutoff, got %d: %v", len(got), tsList(got))
	}
	if !got[0].TS.Equal(base.Add(2 * time.Second)) {
		t.Fatalf("cutoff inclusive boundary wrong: %v", got[0].TS)
	}
}

func TestMemoryListEventsLimit(t *testing.T) {
	base := time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)
	b := memReaderBus(base)
	for i := 0; i < 5; i++ {
		mustPublish(t, b, Event{Project: "p", Phase: PhaseDevelop, Kind: KindProgress})
	}
	ctx := context.Background()

	// limit smaller than available keeps the EARLIEST limit, ascending.
	got, err := b.ListEvents(ctx, Filter{}, time.Time{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2, got %d", len(got))
	}
	if got[0].ID != "id-0" || got[1].ID != "id-1" {
		t.Fatalf("expected earliest two (id-0,id-1), got %s,%s", got[0].ID, got[1].ID)
	}

	// limit <= 0 applies the default (all 5 fit under it).
	got, err = b.ListEvents(ctx, Filter{}, time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("expected default to return all 5, got %d", len(got))
	}
}

func TestMemoryListEventsResolveLimitClamp(t *testing.T) {
	if resolveLimit(0) != DefaultListLimit {
		t.Fatalf("limit 0 should be DefaultListLimit")
	}
	if resolveLimit(-7) != DefaultListLimit {
		t.Fatalf("negative limit should be DefaultListLimit")
	}
	if resolveLimit(MaxListLimit+1) != MaxListLimit {
		t.Fatalf("over-max limit should clamp to MaxListLimit")
	}
	if resolveLimit(50) != 50 {
		t.Fatalf("in-range limit should pass through")
	}
}

func TestMemoryListEventsHistoryCap(t *testing.T) {
	base := time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)
	b := memReaderBus(base, WithHistoryCap(3))
	// Publish 5; only the most recent 3 (id-2,id-3,id-4) should remain.
	for i := 0; i < 5; i++ {
		mustPublish(t, b, Event{Project: "p", Phase: PhaseDevelop, Kind: KindLog})
	}
	got, err := b.ListEvents(context.Background(), Filter{}, time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected cap=3 retained, got %d", len(got))
	}
	if got[0].ID != "id-2" || got[2].ID != "id-4" {
		t.Fatalf("expected oldest dropped, got %s..%s", got[0].ID, got[2].ID)
	}
}

func TestMemoryListEventsClosed(t *testing.T) {
	b := memReaderBus(time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC))
	b.Close()
	_, err := b.ListEvents(context.Background(), Filter{}, time.Time{}, 0)
	if !errors.Is(err, ErrBusClosed) {
		t.Fatalf("expected ErrBusClosed, got %v", err)
	}
}

func TestMemoryListEventsReturnsCopy(t *testing.T) {
	base := time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)
	b := memReaderBus(base)
	mustPublish(t, b, Event{Project: "p", Phase: PhasePlan, Kind: KindStarted})

	got1, err := b.ListEvents(context.Background(), Filter{}, time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Mutate the returned slice's element.
	got1[0].Project = "MUTATED"

	got2, err := b.ListEvents(context.Background(), Filter{}, time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got2[0].Project != "p" {
		t.Fatalf("mutation leaked into internal history: %q", got2[0].Project)
	}
}

// --- PostgresBus (skip-gated real-PG) ---

func TestPostgresListEvents(t *testing.T) {
	bus := pgTestBus(t)
	ctx := context.Background()

	prefix := fmt.Sprintf("listproj-%d", time.Now().UnixNano())
	pA := prefix + "-A"
	pB := prefix + "-B"

	base := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	// Explicit ascending TS so ordering/since assertions are deterministic.
	events := []Event{
		{ID: "e0", TS: base.Add(0 * time.Second), Project: pA, Task: "T1", Phase: PhasePlan, Kind: KindStarted},
		{ID: "e1", TS: base.Add(1 * time.Second), Project: pA, Task: "T2", Phase: PhaseDevelop, Kind: KindLog},
		{ID: "e2", TS: base.Add(2 * time.Second), Project: pA, Task: "T2", Phase: PhaseVerify, Kind: KindInterventionNeeded, Payload: map[string]any{"reason": "T4"}},
		{ID: "e3", TS: base.Add(3 * time.Second), Project: pB, Task: "T1", Phase: PhaseMerge, Kind: KindMerge},
	}
	for i, ev := range events {
		if err := bus.Publish(ctx, ev); err != nil {
			t.Fatalf("publish[%d]: %v", i, err)
		}
	}

	t.Run("projectFilterAscending", func(t *testing.T) {
		got, err := bus.ListEvents(ctx, Filter{Project: pA}, time.Time{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 {
			t.Fatalf("expected 3 for %s, got %d", pA, len(got))
		}
		if got[0].ID != "e0" || got[1].ID != "e1" || got[2].ID != "e2" {
			t.Fatalf("wrong order: %v", []string{got[0].ID, got[1].ID, got[2].ID})
		}
		if got[2].Payload["reason"] != "T4" {
			t.Fatalf("payload not round-tripped: %+v", got[2].Payload)
		}
	})

	t.Run("taskFilter", func(t *testing.T) {
		got, err := bus.ListEvents(ctx, Filter{Project: pA, Task: "T2"}, time.Time{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 T2, got %d", len(got))
		}
	})

	t.Run("interventionOnly", func(t *testing.T) {
		got, err := bus.ListEvents(ctx, Filter{Project: pA, InterventionOnly: true}, time.Time{}, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || !got[0].InterventionNeeded() {
			t.Fatalf("expected 1 intervention, got %+v", got)
		}
	})

	t.Run("sinceCutoff", func(t *testing.T) {
		got, err := bus.ListEvents(ctx, Filter{Project: pA}, base.Add(2*time.Second), 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].ID != "e2" {
			t.Fatalf("expected only e2 after cutoff, got %+v", got)
		}
	})

	t.Run("limitEarliest", func(t *testing.T) {
		got, err := bus.ListEvents(ctx, Filter{Project: pA}, time.Time{}, 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || got[0].ID != "e0" {
			t.Fatalf("expected earliest e0 with limit 1, got %+v", got)
		}
	})

	t.Run("closed", func(t *testing.T) {
		b2 := pgTestBus(t)
		b2.Close()
		if _, err := b2.ListEvents(ctx, Filter{}, time.Time{}, 0); !errors.Is(err, ErrBusClosed) {
			t.Fatalf("expected ErrBusClosed, got %v", err)
		}
	})
}
