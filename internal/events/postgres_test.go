package events

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/everva/conductor-platform/internal/statestore"
	"github.com/jackc/pgx/v5/pgxpool"
)

var pgSchemaCounter atomic.Int64

// pgTestBus spins up a PostgresBus against an isolated schema on
// TEST_DATABASE_URL, running the events migration there. It skips the test when
// TEST_DATABASE_URL is unset so `go test ./...` stays green DB-free.
func pgTestBus(t *testing.T, opts ...PostgresOption) *PostgresBus {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping Postgres LISTEN/NOTIFY tests")
	}
	ctx := context.Background()

	schema := fmt.Sprintf("cp_ev_%d_%d", time.Now().UnixNano(), pgSchemaCounter.Add(1))
	sep := "?"
	for _, c := range dsn {
		if c == '?' {
			sep = "&"
			break
		}
	}
	schemaDSN := dsn + sep + "search_path=" + schema

	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect base: %v", err)
	}
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		base.Close()
		t.Fatalf("create schema: %v", err)
	}
	base.Close()

	if err := statestore.MigrateDSN(ctx, schemaDSN); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	bus, err := NewPostgresBus(ctx, schemaDSN, opts...)
	if err != nil {
		t.Fatalf("new postgres bus: %v", err)
	}
	t.Cleanup(func() {
		bus.Close()
		drop, err := pgxpool.New(ctx, dsn)
		if err != nil {
			t.Logf("cleanup connect: %v", err)
			return
		}
		defer drop.Close()
		if _, err := drop.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
			t.Logf("drop schema: %v", err)
		}
	})
	return bus
}

// TestPostgresPublishPersistsAndNotifies is the headline integration test:
// a LISTEN subscriber receives an event in realtime AND the event is persisted
// as a queryable row. It runs only when TEST_DATABASE_URL is set.
func TestPostgresPublishPersistsAndNotifies(t *testing.T) {
	bus := pgTestBus(t)
	ctx := context.Background()

	ch, cancel, err := bus.Subscribe(ctx, Filter{Project: "proj-1"})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer cancel()

	// Give the LISTEN a moment to be established before publishing.
	time.Sleep(150 * time.Millisecond)

	want := Event{
		Project: "proj-1",
		Task:    "T-9",
		Phase:   PhaseVerify,
		Kind:    KindInterventionNeeded,
		Payload: map[string]any{"reason": "tier T4 needs a human"},
	}
	if err := bus.Publish(ctx, want); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// 1) Subscriber received it in realtime via LISTEN/NOTIFY.
	select {
	case got, ok := <-ch:
		if !ok {
			t.Fatal("subscriber channel closed before delivery")
		}
		if got.Project != "proj-1" || got.Task != "T-9" {
			t.Fatalf("wrong event delivered: %+v", got)
		}
		if got.Phase != PhaseVerify || got.Kind != KindInterventionNeeded {
			t.Fatalf("wrong phase/kind: %+v", got)
		}
		if !got.InterventionNeeded() {
			t.Fatal("expected InterventionNeeded signal")
		}
		if got.ID == "" || got.TS.IsZero() {
			t.Fatalf("event missing assigned ID/TS: %+v", got)
		}
		if got.Payload["reason"] != "tier T4 needs a human" {
			t.Fatalf("payload not delivered: %+v", got.Payload)
		}
		t.Logf("LISTEN subscriber received event id=%s phase=%s kind=%s", got.ID, got.Phase, got.Kind)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for LISTEN/NOTIFY delivery")
	}

	// 2) Event was persisted as a queryable row.
	var (
		count            int
		phase, kind, tsk string
	)
	row := bus.pool.QueryRow(ctx,
		"SELECT count(*), max(phase), max(kind), max(task) FROM events WHERE project = $1", "proj-1")
	if err := row.Scan(&count, &phase, &kind, &tsk); err != nil {
		t.Fatalf("query persisted row: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 persisted row, got %d", count)
	}
	if phase != string(PhaseVerify) || kind != string(KindInterventionNeeded) || tsk != "T-9" {
		t.Fatalf("persisted row mismatch: phase=%s kind=%s task=%s", phase, kind, tsk)
	}
	t.Logf("persisted row confirmed: count=%d phase=%s kind=%s task=%s", count, phase, kind, tsk)
}

// TestPostgresFilterAndFanOut verifies cross-subscriber fan-out plus
// subscriber-side filtering over the single NOTIFY channel.
func TestPostgresFilterAndFanOut(t *testing.T) {
	bus := pgTestBus(t)
	ctx := context.Background()

	all, cancelAll, err := bus.Subscribe(ctx, Filter{})
	if err != nil {
		t.Fatalf("subscribe all: %v", err)
	}
	defer cancelAll()
	mergeOnly, cancelMerge, err := bus.Subscribe(ctx, Filter{Phase: PhaseMerge})
	if err != nil {
		t.Fatalf("subscribe mergeOnly: %v", err)
	}
	defer cancelMerge()

	time.Sleep(150 * time.Millisecond)

	_ = bus.Publish(ctx, Event{Project: "p", Phase: PhaseDevelop, Kind: KindLog})
	_ = bus.Publish(ctx, Event{Project: "p", Phase: PhaseMerge, Kind: KindMerge})

	// `all` sees both (order preserved over a single channel).
	for i, wantPhase := range []Phase{PhaseDevelop, PhaseMerge} {
		select {
		case ev := <-all:
			if ev.Phase != wantPhase {
				t.Fatalf("all[%d] expected %s got %s", i, wantPhase, ev.Phase)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("all[%d] timed out", i)
		}
	}
	// `mergeOnly` sees only the merge event.
	select {
	case ev := <-mergeOnly:
		if ev.Phase != PhaseMerge {
			t.Fatalf("mergeOnly got wrong phase: %s", ev.Phase)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("mergeOnly timed out")
	}
	select {
	case extra := <-mergeOnly:
		t.Fatalf("mergeOnly received an unexpected event: %+v", extra)
	case <-time.After(300 * time.Millisecond):
	}
}
