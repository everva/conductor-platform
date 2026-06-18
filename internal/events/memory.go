package events

import (
	"context"
	"sync"
	"time"
)

// defaultHistoryCap is the default number of recent events MemoryBus retains
// for EventReader.ListEvents replay when WithHistoryCap is not given.
const defaultHistoryCap = 1024

// MemoryBus is an in-memory EventBus for single-process use and tests
// (ADR-0011). Publish fans out synchronously to every matching subscriber over
// a buffered channel; a subscriber whose buffer is full is skipped for that
// event (drop-on-slow) rather than blocking every other subscriber, and its
// Dropped counter is bumped. It is safe for concurrent use.
//
// It additionally retains a bounded history of recently-published events so it
// can satisfy the additive EventReader seam (ListEvents); retention is extra
// bookkeeping and does not change live fan-out (ADR-0021 additive growth).
type MemoryBus struct {
	// bufSize is the per-subscriber channel buffer.
	bufSize int
	// now and newID are injectable so tests get deterministic IDs/timestamps.
	now   func() time.Time
	newID func() string

	mu     sync.Mutex
	closed bool
	nextID int64
	subs   map[int64]*memSub
	// history retains the most recent stamped events (oldest first) up to
	// historyCap for EventReader.ListEvents; the oldest is evicted when over cap.
	history    []Event
	historyCap int
}

// memSub is one live subscription on a MemoryBus.
type memSub struct {
	filter  Filter
	ch      chan Event
	dropped int64
}

// MemoryOption configures a MemoryBus.
type MemoryOption func(*MemoryBus)

// WithBufferSize sets the per-subscriber channel buffer (default 64). A larger
// buffer tolerates burstier subscribers before drop-on-slow kicks in.
func WithBufferSize(n int) MemoryOption {
	return func(b *MemoryBus) {
		if n > 0 {
			b.bufSize = n
		}
	}
}

// WithClock injects the time source (default time.Now().UTC()). Tests use it
// for deterministic timestamps.
func WithClock(now func() time.Time) MemoryOption {
	return func(b *MemoryBus) {
		if now != nil {
			b.now = now
		}
	}
}

// WithIDFunc injects the event-ID generator (default a random ULID-ish hex).
// Tests use it for deterministic IDs.
func WithIDFunc(fn func() string) MemoryOption {
	return func(b *MemoryBus) {
		if fn != nil {
			b.newID = fn
		}
	}
}

// WithHistoryCap sets how many recent events the bus retains for ListEvents
// replay (default defaultHistoryCap). A non-positive n is ignored.
func WithHistoryCap(n int) MemoryOption {
	return func(b *MemoryBus) {
		if n > 0 {
			b.historyCap = n
		}
	}
}

// NewMemoryBus returns an in-memory EventBus.
func NewMemoryBus(opts ...MemoryOption) *MemoryBus {
	b := &MemoryBus{
		bufSize:    64,
		now:        func() time.Time { return time.Now().UTC() },
		newID:      randomID,
		subs:       make(map[int64]*memSub),
		historyCap: defaultHistoryCap,
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Compile-time assertion that *MemoryBus satisfies EventBus.
var _ EventBus = (*MemoryBus)(nil)

// stamp fills an event's ID and TS when empty and validates it. It is shared by
// every implementation so the fill/validate semantics are identical.
func (b *MemoryBus) stamp(ev *Event) error {
	if err := ev.Validate(); err != nil {
		return err
	}
	if ev.TS.IsZero() {
		ev.TS = b.now()
	}
	if ev.ID == "" {
		ev.ID = b.newID()
	}
	return nil
}

// Publish fans ev out to every matching live subscriber. It returns
// ErrInvalidEvent for a bad event and ErrBusClosed if the bus is closed.
func (b *MemoryBus) Publish(ctx context.Context, ev Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := b.stamp(&ev); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return ErrBusClosed
	}
	// Retain the stamped event for ListEvents replay, evicting the oldest when
	// over cap. This is additive bookkeeping; live fan-out below is unchanged.
	b.history = append(b.history, ev)
	if len(b.history) > b.historyCap {
		b.history = b.history[len(b.history)-b.historyCap:]
	}
	for _, s := range b.subs {
		if !s.filter.Matches(ev) {
			continue
		}
		select {
		case s.ch <- ev:
		default:
			// Drop-on-slow: never block the publisher (or other subscribers) on
			// one full subscriber buffer.
			s.dropped++
		}
	}
	return nil
}

// Subscribe registers a filtered subscription and returns its channel plus a
// cancel. The channel is closed exactly once on cancel, ctx-done, or Close.
func (b *MemoryBus) Subscribe(ctx context.Context, filter Filter) (<-chan Event, CancelFunc, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, nil, ErrBusClosed
	}
	id := b.nextID
	b.nextID++
	s := &memSub{filter: filter, ch: make(chan Event, b.bufSize)}
	b.subs[id] = s
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if sub, ok := b.subs[id]; ok {
				delete(b.subs, id)
				close(sub.ch)
			}
		})
	}

	// A goroutine ties the subscription lifetime to ctx so a cancelled context
	// unsubscribes and closes the channel without the caller calling cancel.
	go func() {
		<-ctx.Done()
		cancel()
	}()

	return s.ch, cancel, nil
}

// Close cancels all subscriptions (closing their channels) and marks the bus
// closed; subsequent Publish/Subscribe return ErrBusClosed. It is idempotent.
func (b *MemoryBus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for id, s := range b.subs {
		delete(b.subs, id)
		close(s.ch)
	}
}

// Dropped reports the total number of events dropped across all subscriptions
// due to full buffers. It is a diagnostic for tests/operators, not a contract.
func (b *MemoryBus) Dropped() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	var total int64
	for _, s := range b.subs {
		total += s.dropped
	}
	return total
}
