package main

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/everva/conductor-platform/internal/statestore"
)

// TestExtractDSN_FlagForms proves the global -dsn pre-pass pulls the flag (in all
// of its accepted spellings and positions) OUT of argv while leaving the
// subcommand and its own flags intact — the tricky bit that lets `run`'s
// per-subcommand FlagSets keep parsing --project/--file/--base unchanged.
func TestExtractDSN_FlagForms(t *testing.T) {
	t.Setenv("CONDUCTOR_DSN", "") // isolate from the ambient environment.

	cases := []struct {
		name     string
		argv     []string
		wantDSN  string
		wantRest []string
	}{
		{
			name:     "no dsn keeps argv untouched",
			argv:     []string{"status", "--project", "repo", "--json"},
			wantDSN:  "",
			wantRest: []string{"status", "--project", "repo", "--json"},
		},
		{
			name:     "dsn before subcommand, space form",
			argv:     []string{"-dsn", "postgres://x", "onboard", "owner/repo"},
			wantDSN:  "postgres://x",
			wantRest: []string{"onboard", "owner/repo"},
		},
		{
			name:     "dsn equals form before subcommand",
			argv:     []string{"-dsn=postgres://y", "status", "--project", "p"},
			wantDSN:  "postgres://y",
			wantRest: []string{"status", "--project", "p"},
		},
		{
			name:     "long --dsn form after subcommand, interleaved with sub-flags",
			argv:     []string{"intake", "--project", "p", "--dsn", "postgres://z", "--file", "s.yaml"},
			wantDSN:  "postgres://z",
			wantRest: []string{"intake", "--project", "p", "--file", "s.yaml"},
		},
		{
			name:     "subcommand flags that are not -dsn pass through (no --base eaten)",
			argv:     []string{"onboard", "--base", "main", "owner/repo"},
			wantDSN:  "",
			wantRest: []string{"onboard", "--base", "main", "owner/repo"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dsn, rest, err := extractDSN(tc.argv)
			if err != nil {
				t.Fatalf("extractDSN: %v", err)
			}
			if dsn != tc.wantDSN {
				t.Fatalf("dsn = %q, want %q", dsn, tc.wantDSN)
			}
			if !reflect.DeepEqual(rest, tc.wantRest) {
				t.Fatalf("rest = %v, want %v", rest, tc.wantRest)
			}
		})
	}
}

// TestExtractDSN_EnvFallback proves CONDUCTOR_DSN supplies the default and an
// explicit flag overrides it (flag > env), mirroring the daemon's precedence.
func TestExtractDSN_EnvFallback(t *testing.T) {
	t.Setenv("CONDUCTOR_DSN", "postgres://from-env")

	dsn, rest, err := extractDSN([]string{"status", "--project", "p"})
	if err != nil {
		t.Fatalf("extractDSN: %v", err)
	}
	if dsn != "postgres://from-env" {
		t.Fatalf("env fallback dsn = %q, want postgres://from-env", dsn)
	}
	if !reflect.DeepEqual(rest, []string{"status", "--project", "p"}) {
		t.Fatalf("rest = %v", rest)
	}

	dsn, _, err = extractDSN([]string{"-dsn", "postgres://from-flag", "status", "--project", "p"})
	if err != nil {
		t.Fatalf("extractDSN: %v", err)
	}
	if dsn != "postgres://from-flag" {
		t.Fatalf("flag must override env: dsn = %q, want postgres://from-flag", dsn)
	}
}

// TestExtractDSN_MissingValueErrors proves a trailing -dsn with no value is a
// clear error rather than silently swallowing the subcommand.
func TestExtractDSN_MissingValueErrors(t *testing.T) {
	t.Setenv("CONDUCTOR_DSN", "")
	if _, _, err := extractDSN([]string{"status", "--project", "p", "-dsn"}); err == nil {
		t.Fatal("trailing -dsn with no value must error")
	}
}

// TestNewStore_EmptyDSNUsesMemory proves the default (empty DSN) keeps the Faz-1a
// in-memory store and returns a non-nil no-op closer, so existing CLI behavior and
// the in-memory tests are unchanged.
func TestNewStore_EmptyDSNUsesMemory(t *testing.T) {
	store, closer, err := newStore(context.Background(), "")
	if err != nil {
		t.Fatalf("newStore(empty): %v", err)
	}
	if closer == nil {
		t.Fatal("closer is nil; want a non-nil no-op closer for memory")
	}
	if _, ok := store.(*statestore.MemoryStore); !ok {
		t.Fatalf("store type = %T, want *statestore.MemoryStore for empty dsn", store)
	}
	closer() // must not panic for the memory backend.
}

// TestRealDB_CrossProcessRoundTrip is the OPTIONAL real-database test proving the
// big payoff: a project onboarded through one Postgres-backed store is visible to
// a SEPARATE store instance opened on the same DSN — i.e. across processes, which
// is impossible with the in-memory store. It onboards via one *app (store A),
// then builds a brand-new app on a fresh NewPostgresStore (store B) and runs
// `status`, asserting B sees A's project + task. It SKIPS cleanly when
// TEST_DATABASE_URL is unset, so the default `go test ./...` stays DB-free.
func TestRealDB_CrossProcessRoundTrip(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL unset; skipping real-DB cross-process test")
	}
	ctx := context.Background()

	// Migrate once up front (newStore also migrates; this is idempotent).
	if err := statestore.MigrateDSN(ctx, dsn); err != nil {
		t.Fatalf("MigrateDSN: %v", err)
	}

	const repo = "owner/p3-4-roundtrip"
	const projID = "p3-4-roundtrip"

	// --- "Process A": onboard + intake through one Postgres-backed store. ---
	storeA, closeA, err := newStore(ctx, dsn)
	if err != nil {
		t.Fatalf("newStore A: %v", err)
	}
	if _, ok := storeA.(*statestore.PostgresStore); !ok {
		closeA()
		t.Fatalf("store A type = %T, want *statestore.PostgresStore", storeA)
	}
	appA := &app{store: storeA, ctrl: NewMemoryController(), out: &bytes.Buffer{}}

	p, err := appA.onboard(ctx, repo, "develop")
	if err != nil {
		closeA()
		t.Fatalf("onboard via store A: %v", err)
	}
	if p.ID != projID {
		closeA()
		t.Fatalf("project id = %q, want %q", p.ID, projID)
	}
	scn := writeScenario(t, richScenario("RT-1", "x", "T1"))
	if _, err := appA.intake(ctx, projID, scn); err != nil {
		closeA()
		t.Fatalf("intake via store A: %v", err)
	}
	closeA() // CLOSE store A entirely — simulating process A exiting.

	// --- "Process B": a fresh store on the SAME dsn must SEE A's writes. ---
	storeB, closeB, err := newStore(ctx, dsn)
	if err != nil {
		t.Fatalf("newStore B: %v", err)
	}
	defer closeB()
	var outB bytes.Buffer
	appB := &app{store: storeB, ctrl: NewMemoryController(), out: &outB}

	if err := appB.status(ctx, projID, false); err != nil {
		t.Fatalf("status via store B: %v", err)
	}
	if got := outB.String(); !bytes.Contains([]byte(got), []byte("RT-1")) {
		t.Fatalf("store B (separate process) did not see store A's task RT-1:\n%s", got)
	}

	// Direct store-level assertion too: the project row is durably visible.
	projects, err := storeB.ListProjects(ctx)
	if err != nil {
		t.Fatalf("store B ListProjects: %v", err)
	}
	found := false
	for _, pr := range projects {
		if pr.ID == projID && pr.Repo == repo {
			found = true
		}
	}
	if !found {
		t.Fatalf("project %q not visible to separate store B; projects=%+v", projID, projects)
	}
}
