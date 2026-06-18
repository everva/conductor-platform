package holdout

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/everva/conductor-platform/internal/statestore"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgTestPool spins up an isolated Postgres schema (so cases never collide), runs
// the statestore migrations there (which include 00005_holdouts), and returns a
// pool scoped to that schema plus a teardown. It skips cleanly when
// TEST_DATABASE_URL is unset so the offline gate stays DB-free.
func pgTestPool(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping Postgres holdout tests")
	}
	ctx := context.Background()

	schema := fmt.Sprintf("cp_holdout_%d", time.Now().UnixNano())
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect base: %v", err)
	}
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		base.Close()
		t.Fatalf("create schema: %v", err)
	}
	base.Close()

	sep := "?"
	if strings.ContainsRune(dsn, '?') {
		sep = "&"
	}
	schemaDSN := dsn + sep + "search_path=" + schema

	if err := statestore.MigrateDSN(ctx, schemaDSN); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(ctx, schemaDSN)
	if err != nil {
		t.Fatalf("connect schema pool: %v", err)
	}
	cleanup := func() {
		pool.Close()
		drop, err := pgxpool.New(ctx, dsn)
		if err != nil {
			t.Logf("cleanup connect: %v", err)
			return
		}
		defer drop.Close()
		if _, err := drop.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
			t.Logf("drop schema: %v", err)
		}
	}
	return pool, cleanup
}

func TestPGStore_Fetch_RoundTrip(t *testing.T) {
	pool, cleanup := pgTestPool(t)
	defer cleanup()
	ctx := context.Background()

	rows := []struct{ id, path, content string }{
		{"A-1", "holdout_test.go", "package x\n"},
		{"A-1", "sub/extra_test.go", "package x\n// extra\n"},
		{"B-2", "other_test.go", "package y\n"},
	}
	for _, r := range rows {
		if _, err := pool.Exec(ctx, "INSERT INTO holdouts (id, path, content) VALUES ($1,$2,$3)", r.id, r.path, []byte(r.content)); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	s, err := NewPG(pool)
	if err != nil {
		t.Fatalf("NewPG: %v", err)
	}
	h, err := s.Fetch(ctx, "pg://holdouts/A-1")
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if h.Name != "A-1" {
		t.Fatalf("Name = %q, want A-1", h.Name)
	}
	if len(h.Files) != 2 {
		t.Fatalf("Files = %d, want 2: %v", len(h.Files), keys(h.Files))
	}
	if got := string(h.Files["holdout_test.go"]); got != "package x\n" {
		t.Fatalf("content = %q", got)
	}
	if _, ok := h.Files["sub/extra_test.go"]; !ok {
		t.Fatalf("missing nested file; got %v", keys(h.Files))
	}

	// Trailing path elements after the id are ignored.
	if _, err := s.Fetch(ctx, "pg://holdouts/A-1/spec.yaml"); err != nil {
		t.Fatalf("Fetch with trailing path: %v", err)
	}
}

func TestPGStore_Fetch_MissingID_Errors(t *testing.T) {
	pool, cleanup := pgTestPool(t)
	defer cleanup()
	s, _ := NewPG(pool)
	if _, err := s.Fetch(context.Background(), "pg://holdouts/NOPE"); err == nil {
		t.Fatalf("missing id must error")
	}
}

func TestPGStore_Fetch_PathTraversalRow_Rejected(t *testing.T) {
	pool, cleanup := pgTestPool(t)
	defer cleanup()
	ctx := context.Background()

	if _, err := pool.Exec(ctx, "INSERT INTO holdouts (id, path, content) VALUES ($1,$2,$3)",
		"EVIL", "../../escape.go", []byte("package x\n")); err != nil {
		t.Fatalf("insert: %v", err)
	}
	s, _ := NewPG(pool)
	if _, err := s.Fetch(ctx, "pg://holdouts/EVIL"); err == nil {
		t.Fatalf("path-traversal row must be rejected")
	}
}

func TestPGStore_Fetch_EmptyRef_Skips(t *testing.T) {
	// No DB needed for the empty-ref skip; build a store over a nil-safe path by
	// gating on the pool only when present.
	pool, cleanup := pgTestPool(t)
	defer cleanup()
	s, _ := NewPG(pool)
	h, err := s.Fetch(context.Background(), "")
	if err != nil {
		t.Fatalf("empty ref must not error: %v", err)
	}
	if len(h.Files) != 0 {
		t.Fatalf("empty ref must yield no files")
	}
}

func TestPGStore_NilPool_Errors(t *testing.T) {
	if _, err := NewPG(nil); err == nil {
		t.Fatalf("nil pool must error")
	}
}

func TestPGHoldoutID(t *testing.T) {
	cases := map[string]string{
		"pg://holdouts/A-1":           "A-1",
		"pg://holdouts/A-1/spec.yaml": "A-1",
		"pg://holdouts/B-2/":          "B-2",
	}
	for ref, want := range cases {
		got, err := pgHoldoutID(ref)
		if err != nil {
			t.Fatalf("ref %q: %v", ref, err)
		}
		if got != want {
			t.Fatalf("ref %q: id = %q, want %q", ref, got, want)
		}
	}
	for _, bad := range []string{"pg://", "pg://holdouts/", "pg://wrong/A-1"} {
		if _, err := pgHoldoutID(bad); err == nil {
			t.Fatalf("ref %q must be rejected", bad)
		}
	}
}
