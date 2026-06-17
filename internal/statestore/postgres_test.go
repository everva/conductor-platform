package statestore

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// schemaCounter gives each conformance subtest a unique, isolated Postgres
// schema so cases run against a fresh namespace without colliding.
var schemaCounter atomic.Int64

// TestPostgresConformance runs the SAME shared conformance suite as the
// in-memory store against a real Postgres, proving both implementations satisfy
// the frozen contract identically. It skips cleanly when TEST_DATABASE_URL is
// unset so `go test ./...` stays green on a machine with no database.
func TestPostgresConformance(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping Postgres integration tests")
	}

	runConformanceSuite(t, func(t *testing.T) (StateStore, func()) {
		ctx := context.Background()

		// Each subtest gets its own schema so state never leaks between cases.
		schema := fmt.Sprintf("cp_test_%d_%d", time.Now().UnixNano(), schemaCounter.Add(1))
		schemaDSN := withSearchPath(t, dsn, schema)

		// Create the schema using a short-lived store on the base DSN.
		base, err := NewPostgresStore(ctx, dsn)
		if err != nil {
			t.Fatalf("connect base: %v", err)
		}
		if _, err := base.pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
			base.Close()
			t.Fatalf("create schema %s: %v", schema, err)
		}
		base.Close()

		// Apply migrations within the isolated schema, then open the store there.
		if err := MigrateDSN(ctx, schemaDSN); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		s, err := NewPostgresStore(ctx, schemaDSN)
		if err != nil {
			t.Fatalf("connect store: %v", err)
		}

		cleanup := func() {
			s.Close()
			drop, err := NewPostgresStore(ctx, dsn)
			if err != nil {
				t.Logf("cleanup connect: %v", err)
				return
			}
			defer drop.Close()
			if _, err := drop.pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
				t.Logf("drop schema %s: %v", schema, err)
			}
		}
		return s, cleanup
	})
}

// withSearchPath returns dsn with the connection's search_path set to schema so
// all queries (including goose's migration bookkeeping) land in that schema.
func withSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	sep := "?"
	if containsRune(dsn, '?') {
		sep = "&"
	}
	return dsn + sep + "search_path=" + schema
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}
