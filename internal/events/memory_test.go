package events

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// recvWithin returns the next event on ch or fails after a short timeout.
func recvWithin(t *testing.T, ch <-chan Event, d time.Duration) (Event, bool) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		return ev, ok
	case <-time.After(d):
		t.Fatal("timed out waiting for event")
		return Event{}, false
	}
}

func newTestBus(opts ...MemoryOption) *MemoryBus {
	fixed := time.Date(2026, 6, 18, 0, 0, 0, 0, time.UTC)
	base := []MemoryOption{
		WithClock(func() time.Time { return fixed }),
	}
	return NewMemoryBus(append(base, opts...)...)
}

func TestMemoryPublishSubscribe(t *testing.T) {
	ctx := context.Background()
	b := newTestBus()
	ch, cancel, err := b.Subscribe(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	if err := b.Publish(ctx, Event{Project: "p", Phase: PhaseDevelop, Kind: KindLog}); err != nil {
		t.Fatal(err)
	}
	ev, ok := recvWithin(t, ch, time.Second)
	if !ok {
		t.Fatal("channel closed unexpectedly")
	}
	if ev.Project != "p" || ev.Phase != PhaseDevelop {
		t.Fatalf("wrong event: %+v", ev)
	}
	if ev.ID == "" {
		t.Error("bus should have assigned an ID")
	}
	if ev.TS.IsZero() {
		t.Error("bus should have stamped TS")
	}
}

func TestMemoryFanOut(t *testing.T) {
	ctx := context.Background()
	b := newTestBus()
	const n = 5
	chans := make([]<-chan Event, n)
	for i := range chans {
		ch, cancel, err := b.Subscribe(ctx, Filter{})
		if err != nil {
			t.Fatal(err)
		}
		defer cancel()
		chans[i] = ch
	}
	if err := b.Publish(ctx, Event{Project: "p", Phase: PhaseMerge, Kind: KindMerge}); err != nil {
		t.Fatal(err)
	}
	for i, ch := range chans {
		ev, ok := recvWithin(t, ch, time.Second)
		if !ok {
			t.Fatalf("sub %d closed", i)
		}
		if ev.Kind != KindMerge {
			t.Fatalf("sub %d wrong event: %+v", i, ev)
		}
	}
}

func TestMemoryFilter(t *testing.T) {
	ctx := context.Background()
	b := newTestBus()

	only, cancel, err := b.Subscribe(ctx, Filter{Project: "p1", Phase: PhaseVerify})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	intv, cancel2, err := b.Subscribe(ctx, Filter{InterventionOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel2()

	// Non-matching for `only` (wrong project), non-matching for `intv` (not intervention).
	_ = b.Publish(ctx, Event{Project: "p2", Phase: PhaseVerify, Kind: KindLog})
	// Matching for `only`, non-matching for `intv`.
	_ = b.Publish(ctx, Event{Project: "p1", Phase: PhaseVerify, Kind: KindDecision})
	// Matching for `intv`, non-matching for `only` (wrong project+phase).
	_ = b.Publish(ctx, Event{Project: "p2", Phase: PhaseReview, Kind: KindInterventionNeeded})

	ev, ok := recvWithin(t, only, time.Second)
	if !ok || ev.Project != "p1" || ev.Kind != KindDecision {
		t.Fatalf("filtered sub got wrong/closed: %+v ok=%v", ev, ok)
	}
	// `only` must not receive a second event.
	select {
	case extra := <-only:
		t.Fatalf("filtered sub received unexpected event: %+v", extra)
	case <-time.After(100 * time.Millisecond):
	}

	iev, ok := recvWithin(t, intv, time.Second)
	if !ok || !iev.InterventionNeeded() {
		t.Fatalf("intervention sub got wrong/closed: %+v ok=%v", iev, ok)
	}
}

func TestMemoryUnsubscribeClosesChannel(t *testing.T) {
	ctx := context.Background()
	b := newTestBus()
	ch, cancel, err := b.Subscribe(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	// Channel must close.
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected closed channel after cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("channel not closed after cancel")
	}
	// Cancel is idempotent.
	cancel()
	// Publishing after unsubscribe is fine (no panic, no delivery).
	if err := b.Publish(ctx, Event{Project: "p", Phase: PhaseDevelop, Kind: KindLog}); err != nil {
		t.Fatal(err)
	}
}

func TestMemoryCtxCancelUnsubscribes(t *testing.T) {
	ctx, cancelCtx := context.WithCancel(context.Background())
	b := newTestBus()
	ch, _, err := b.Subscribe(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	cancelCtx()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected closed channel after ctx cancel")
		}
	case <-time.After(time.Second):
		t.Fatal("channel not closed after ctx cancel")
	}
}

func TestMemoryPublishInvalid(t *testing.T) {
	b := newTestBus()
	err := b.Publish(context.Background(), Event{Phase: PhaseDevelop, Kind: KindLog})
	if !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("expected ErrInvalidEvent, got %v", err)
	}
}

func TestMemoryClosed(t *testing.T) {
	ctx := context.Background()
	b := newTestBus()
	ch, _, err := b.Subscribe(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	b.Close()
	// Subscriber channel closed by Close.
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected closed channel after bus Close")
		}
	case <-time.After(time.Second):
		t.Fatal("channel not closed after Close")
	}
	if err := b.Publish(ctx, Event{Project: "p", Phase: PhaseDevelop, Kind: KindLog}); !errors.Is(err, ErrBusClosed) {
		t.Fatalf("expected ErrBusClosed on publish, got %v", err)
	}
	if _, _, err := b.Subscribe(ctx, Filter{}); !errors.Is(err, ErrBusClosed) {
		t.Fatalf("expected ErrBusClosed on subscribe, got %v", err)
	}
	b.Close() // idempotent
}

func TestMemoryDropOnSlow(t *testing.T) {
	ctx := context.Background()
	b := newTestBus(WithBufferSize(1))
	_, cancel, err := b.Subscribe(ctx, Filter{}) // never drained
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	// Buffer is 1; publish several. Publisher must not block.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10; i++ {
			_ = b.Publish(ctx, Event{Project: "p", Phase: PhaseDevelop, Kind: KindLog})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publisher blocked on a slow subscriber (drop-on-slow failed)")
	}
	if b.Dropped() == 0 {
		t.Error("expected some dropped events for the undrained subscriber")
	}
}

func TestMemoryConcurrentPublish(t *testing.T) {
	ctx := context.Background()
	b := newTestBus(WithBufferSize(1024))
	ch, cancel, err := b.Subscribe(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	const writers, perWriter = 8, 50
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				_ = b.Publish(ctx, Event{Project: "p", Phase: PhaseDevelop, Kind: KindProgress})
			}
		}()
	}
	want := writers * perWriter
	got := 0
	doneRecv := make(chan struct{})
	go func() {
		for got < want {
			if _, ok := recvWithin(t, ch, 2*time.Second); !ok {
				break
			}
			got++
		}
		close(doneRecv)
	}()
	wg.Wait()
	<-doneRecv
	if got != want {
		t.Fatalf("expected %d events, received %d", want, got)
	}
}

func TestMemoryDeterministicID(t *testing.T) {
	ctx := context.Background()
	b := newTestBus(WithIDFunc(func() string { return "fixed-id" }))
	ch, cancel, err := b.Subscribe(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	_ = b.Publish(ctx, Event{Project: "p", Phase: PhaseDevelop, Kind: KindLog})
	ev, _ := recvWithin(t, ch, time.Second)
	if ev.ID != "fixed-id" {
		t.Fatalf("expected injected ID, got %q", ev.ID)
	}
}
