package events

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// notifyChannel is the Postgres NOTIFY channel the bus publishes to and
// subscribers LISTEN on. A single channel carries every event; filtering is
// applied subscriber-side (the payload is small JSON), which keeps the LISTEN
// wiring trivial and matches ADR-0011's "single central collection point".
const notifyChannel = "conductor_events"

// PostgresBus is a Postgres-backed EventBus (ADR-0011). Publish persists the
// event to the events table AND pg_notify()s it on notifyChannel in one round
// trip, so subscribers — including ones on other processes/hosts — receive it in
// realtime. Subscribe holds a dedicated pooled connection LISTENing on the
// channel and fans matching notifications out to its channel.
//
// The events table is created by the statestore goose migrations
// (00002_events.sql); call statestore.MigrateDSN once before use, exactly like
// the rest of the schema.
type PostgresBus struct {
	pool  *pgxpool.Pool
	now   func() time.Time
	newID func() string

	mu      sync.Mutex
	closed  bool
	cancels map[int64]func()
	nextID  int64
	wg      sync.WaitGroup
	bufSize int
}

// Compile-time assertion that *PostgresBus satisfies EventBus.
var _ EventBus = (*PostgresBus)(nil)

// PostgresOption configures a PostgresBus.
type PostgresOption func(*PostgresBus)

// WithPGBufferSize sets the per-subscriber channel buffer (default 64).
func WithPGBufferSize(n int) PostgresOption {
	return func(b *PostgresBus) {
		if n > 0 {
			b.bufSize = n
		}
	}
}

// WithPGClock injects the time source (default time.Now().UTC()).
func WithPGClock(now func() time.Time) PostgresOption {
	return func(b *PostgresBus) {
		if now != nil {
			b.now = now
		}
	}
}

// WithPGIDFunc injects the event-ID generator (default a random hex ID).
func WithPGIDFunc(fn func() string) PostgresOption {
	return func(b *PostgresBus) {
		if fn != nil {
			b.newID = fn
		}
	}
}

// NewPostgresBus opens a pgx pool against dsn, verifies connectivity, and
// returns a Postgres-backed EventBus. The caller owns it and must Close it.
func NewPostgresBus(ctx context.Context, dsn string, opts ...PostgresOption) (*PostgresBus, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("events: open pgx pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("events: ping postgres: %w", err)
	}
	return NewPostgresBusFromPool(pool, opts...), nil
}

// NewPostgresBusFromPool wraps an existing pgxpool.Pool. The bus does NOT take
// ownership of the pool's lifecycle beyond its own subscriptions: Close stops
// the bus's LISTENers but leaves the pool open for the caller to close, so a
// PostgresStore and a PostgresBus can share one pool.
func NewPostgresBusFromPool(pool *pgxpool.Pool, opts ...PostgresOption) *PostgresBus {
	b := &PostgresBus{
		pool:    pool,
		now:     func() time.Time { return time.Now().UTC() },
		newID:   randomID,
		cancels: make(map[int64]func()),
		bufSize: 64,
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// stamp fills an event's ID/TS when empty and validates it (shared semantics
// with MemoryBus).
func (b *PostgresBus) stamp(ev *Event) error {
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

// Publish persists ev and pg_notify()s its JSON in a single statement so the
// row and the realtime push are atomic from the subscriber's perspective. It
// returns ErrInvalidEvent for a bad event and ErrBusClosed if the bus is closed.
func (b *PostgresBus) Publish(ctx context.Context, ev Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := b.stamp(&ev); err != nil {
		return err
	}
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return ErrBusClosed
	}

	payload, err := json.Marshal(ev.Payload)
	if err != nil {
		return fmt.Errorf("events: marshal payload: %w", err)
	}
	notify, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("events: marshal notify: %w", err)
	}

	// Insert the row, then NOTIFY with the full event JSON. A CTE keeps it to one
	// round trip; pg_notify is transaction-scoped so a failed insert sends no
	// notification (no phantom pushes for unpersisted events).
	const q = `
WITH ins AS (
    INSERT INTO events (id, ts, project, task, phase, kind, payload)
    VALUES ($1, $2, $3, $4, $5, $6, $7)
    RETURNING id
)
SELECT pg_notify($8, $9) FROM ins`
	_, err = b.pool.Exec(ctx, q,
		ev.ID, ev.TS, ev.Project, ev.Task, string(ev.Phase), string(ev.Kind), payload,
		notifyChannel, string(notify))
	if err != nil {
		return fmt.Errorf("events: publish %q: %w", ev.ID, err)
	}
	return nil
}

// Subscribe acquires a dedicated pooled connection, issues LISTEN on the notify
// channel, and spins a goroutine that waits for notifications, decodes them,
// applies the filter, and fans matching events out to the returned channel. The
// channel closes on cancel, ctx-done, or Close. Drop-on-slow: a full subscriber
// buffer drops the event rather than wedging the LISTENer.
func (b *PostgresBus) Subscribe(ctx context.Context, filter Filter) (<-chan Event, CancelFunc, error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, nil, ErrBusClosed
	}
	b.mu.Unlock()

	conn, err := b.pool.Acquire(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("events: acquire listen conn: %w", err)
	}
	if _, err := conn.Exec(ctx, "LISTEN "+notifyChannel); err != nil {
		conn.Release()
		return nil, nil, fmt.Errorf("events: LISTEN: %w", err)
	}

	subCtx, cancelCtx := context.WithCancel(ctx)
	out := make(chan Event, b.bufSize)

	var closeOnce sync.Once
	closeOut := func() { closeOnce.Do(func() { close(out) }) }

	b.mu.Lock()
	id := b.nextID
	b.nextID++
	// The registered cancel stops the goroutine; the goroutine itself releases
	// the conn and closes out so cleanup happens in exactly one place.
	b.cancels[id] = cancelCtx
	b.mu.Unlock()

	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer closeOut()
		defer conn.Release()
		defer func() {
			b.mu.Lock()
			delete(b.cancels, id)
			b.mu.Unlock()
		}()
		for {
			n, werr := conn.Conn().WaitForNotification(subCtx)
			if werr != nil {
				// Context cancelled (unsubscribe/close) or connection died: stop.
				return
			}
			var ev Event
			if jerr := json.Unmarshal([]byte(n.Payload), &ev); jerr != nil {
				// A malformed payload is skipped, not fatal — keep the stream alive.
				continue
			}
			if !filter.Matches(ev) {
				continue
			}
			select {
			case out <- ev:
			case <-subCtx.Done():
				return
			default:
				// Drop-on-slow.
			}
		}
	}()

	return out, CancelFunc(cancelCtx), nil
}

// Close stops all LISTENers (closing their channels) and marks the bus closed;
// subsequent Publish/Subscribe return ErrBusClosed. It waits for the LISTEN
// goroutines to release their connections. It does NOT close the underlying
// pool (the caller owns it). It is idempotent.
func (b *PostgresBus) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	cancels := make([]func(), 0, len(b.cancels))
	for _, c := range b.cancels {
		cancels = append(cancels, c)
	}
	b.mu.Unlock()

	for _, c := range cancels {
		c()
	}
	b.wg.Wait()
}
