package statestore

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

// TestConcurrentMigrate proves the migration advisory lock (runGooseUp's
// WithSessionLocker) serializes concurrent migrators. BOTH the daemon
// (cmd/conductor) and the API gateway (cmd/conductor-api) run Migrate on startup,
// so a simultaneous cold start against a FRESH database must not race two goose Up
// runs into a duplicate-DDL / goose_db_version conflict. With the lock, every
// concurrent migrator succeeds and the schema ends up correct; without it, this
// flakes (see the WithSessionLocker comment in postgres.go). Skips cleanly when
// TEST_DATABASE_URL is unset, like the rest of the Postgres suite.
func TestConcurrentMigrate(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping Postgres integration test")
	}
	ctx := context.Background()

	// A fresh, UNMIGRATED schema — the worst case (nothing applied yet, so every
	// migrator wants to create the same tables).
	schema := fmt.Sprintf("cp_migrate_race_%d_%d", time.Now().UnixNano(), schemaCounter.Add(1))
	schemaDSN := withSearchPath(t, dsn, schema)

	base, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("connect base: %v", err)
	}
	if _, err := base.pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		base.Close()
		t.Fatalf("create schema %s: %v", schema, err)
	}
	base.Close()
	t.Cleanup(func() {
		drop, err := NewPostgresStore(ctx, dsn)
		if err != nil {
			return
		}
		defer drop.Close()
		_, _ = drop.pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	})

	// N migrators released at once to maximize contention.
	const n = 6
	var wg sync.WaitGroup
	errs := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = MigrateDSN(ctx, schemaDSN)
		}(i)
	}
	close(start)
	wg.Wait()

	for i, e := range errs {
		if e != nil {
			t.Fatalf("concurrent migrator %d failed (advisory lock not serializing?): %v", i, e)
		}
	}

	// Schema is correct: a store opens and the projects table is usable.
	s, err := NewPostgresStore(ctx, schemaDSN)
	if err != nil {
		t.Fatalf("open store after concurrent migrate: %v", err)
	}
	defer s.Close()
	if _, err := s.ListProjects(ctx); err != nil {
		t.Fatalf("ListProjects after concurrent migrate: %v", err)
	}
}
