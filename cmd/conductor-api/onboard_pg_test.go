package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/everva/conductor-platform/internal/statestore"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These are the real-Postgres regression tests for the gateway onboard-500 bug
// (found in the live demo): conductorctl onboard and the in-memory HTTP onboard
// both worked, but a PG-backed gateway returned 500 on POST /projects. Root cause:
// the gateway opened the store but — unlike the daemon — never ran migrations, so
// against a fresh/unmigrated DB every store op failed with a swallowed
// `relation "projects" does not exist`. The fix: newStore now runs an idempotent
// Migrate on startup, and 500s log their real cause server-side (apiServer.logger).
//
// control_test.go exercises the same handlers over an in-memory store, which never
// reproduces a PG schema/wiring fault — hence these need a real database. They skip
// cleanly when TEST_DATABASE_URL is unset (matching internal/statestore), so
// `go test ./...` stays green without a DB; they are part of the local real-PG gate.

var pgSchemaCounter atomic.Int64

// freshSchema creates an isolated, UNMIGRATED Postgres schema and returns it, a DSN
// scoped to it (via search_path), and a cleanup that drops it. "Unmigrated" is the
// point: it reproduces the demo's fresh-DB condition so the test proves the gateway
// makes the schema usable itself.
func freshSchema(t *testing.T, ctx context.Context, baseDSN string) (schema, schemaDSN string, drop func()) {
	t.Helper()
	schema = fmt.Sprintf("cp_api_test_%d_%d", time.Now().UnixNano(), pgSchemaCounter.Add(1))

	pool, err := pgxpool.New(ctx, baseDSN)
	if err != nil {
		t.Fatalf("connect base to create schema: %v", err)
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		pool.Close()
		t.Fatalf("create schema %s: %v", schema, err)
	}
	pool.Close()

	sep := "?"
	if strings.ContainsRune(baseDSN, '?') {
		sep = "&"
	}
	schemaDSN = baseDSN + sep + "search_path=" + schema

	drop = func() {
		dp, err := pgxpool.New(ctx, baseDSN)
		if err != nil {
			t.Logf("cleanup connect: %v", err)
			return
		}
		defer dp.Close()
		if _, err := dp.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
			t.Logf("drop schema %s: %v", schema, err)
		}
	}
	return schema, schemaDSN, drop
}

// testLogger discards output — tests assert behavior, not log lines.
func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func requirePGDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping Postgres integration test")
	}
	return dsn
}

// TestNewStorePG_MigratesFreshSchema is the direct regression test: newStore,
// pointed at a FRESH (unmigrated) database, must leave the schema usable. Before
// the migrate-on-startup fix, ListProjects here failed with
// `relation "projects" does not exist` — the exact swallowed 500 from the demo.
func TestNewStorePG_MigratesFreshSchema(t *testing.T) {
	dsn := requirePGDSN(t)
	ctx := context.Background()

	schema, schemaDSN, drop := freshSchema(t, ctx, dsn)
	defer drop()

	store, closeStore, err := newStore(ctx, config{dsn: schemaDSN}, testLogger())
	if err != nil {
		t.Fatalf("newStore against fresh schema %s: %v", schema, err)
	}
	defer closeStore()

	// The bug surfaced exactly here before the fix.
	if _, err := store.ListProjects(ctx); err != nil {
		t.Fatalf("ListProjects on a freshly-wired gateway store (schema not migrated?): %v", err)
	}
}

// TestHandleOnboardPG drives the REAL onboard handler over a Postgres-backed
// apiServer — the path control_test.go's in-memory store never exercised. Create →
// 201; idempotent re-onboard of the same repo → 200; the row is actually persisted.
func TestHandleOnboardPG(t *testing.T) {
	dsn := requirePGDSN(t)
	ctx := context.Background()

	_, schemaDSN, drop := freshSchema(t, ctx, dsn)
	defer drop()

	store, closeStore, err := newStore(ctx, config{dsn: schemaDSN}, testLogger())
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	defer closeStore()

	srv := &apiServer{store: store, token: testToken, clock: fixedClock, logger: testLogger()}
	h := srv.routes()

	onboard := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/projects",
			strings.NewReader(`{"repo":"everva/pg-onboard","base_branch":"develop"}`))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := onboard()
	if rec.Code != http.StatusCreated {
		t.Fatalf("first onboard: status=%d body=%s", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	if !strings.Contains(rec.Body.String(), `"repo":"everva/pg-onboard"`) {
		t.Fatalf("first onboard body missing repo: %s", strings.TrimSpace(rec.Body.String()))
	}

	rec = onboard()
	if rec.Code != http.StatusOK {
		t.Fatalf("idempotent re-onboard: want 200, status=%d body=%s", rec.Code, strings.TrimSpace(rec.Body.String()))
	}

	// The project is persisted (exactly one row, idempotent).
	projects, err := store.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects after onboard: %v", err)
	}
	if len(projects) != 1 || projects[0].Repo != "everva/pg-onboard" {
		t.Fatalf("want exactly one persisted project for everva/pg-onboard, got %+v", projects)
	}
}

// TestAgentLeaseStatusFlipPG is the GERÇEK-PG round-trip proof of M1: against a REAL
// Postgres-backed gateway, leasing a task flips its persisted status to "running" so the
// editor's sessions tree — which reads GET /projects/{id}/tasks (Task.Status) — matches the
// board's lease-derived "running" instead of showing a stale "todo". An owner release with no
// terminal verdict reverts it to "ready" so it is re-runnable. control_test.go / agent_test.go
// prove this over the in-memory store; this exercises the same path through the real DB the
// live davinci gateway uses.
func TestAgentLeaseStatusFlipPG(t *testing.T) {
	dsn := requirePGDSN(t)
	ctx := context.Background()

	_, schemaDSN, drop := freshSchema(t, ctx, dsn)
	defer drop()

	store, closeStore, err := newStore(ctx, config{dsn: schemaDSN}, testLogger())
	if err != nil {
		t.Fatalf("newStore: %v", err)
	}
	defer closeStore()

	mustCreate(t, store.CreateProject(ctx, statestore.Project{ID: "p", Repo: "everva/p", BaseBranch: "develop", Readiness: "ready"}))
	mustCreate(t, store.CreateTask(ctx, statestore.Task{ID: "T-1", ProjectID: "p", Lane: "backend", Tier: "T2", Status: "todo"}))

	srv := &apiServer{store: store, token: testToken, clock: fixedClock, logger: testLogger()}
	h := srv.routes()

	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// Lease the task through the REST agent API (the davinci host's exact path).
	if rec := call(http.MethodPost, "/projects/p/agent/lease", `{"host_id":"davinci","capabilities":["backend"]}`); rec.Code != http.StatusOK {
		t.Fatalf("lease: status=%d body=%s", rec.Code, strings.TrimSpace(rec.Body.String()))
	}

	// The editor's data source — GET /projects/{id}/tasks — now reports the task running.
	tasks := call(http.MethodGet, "/projects/p/tasks", "")
	if tasks.Code != http.StatusOK || !strings.Contains(tasks.Body.String(), `"status":"running"`) {
		t.Fatalf("after lease, /tasks should show running (editor tree source): status=%d body=%s", tasks.Code, strings.TrimSpace(tasks.Body.String()))
	}
	if got, _ := store.GetTask(ctx, "T-1"); got.Status != "running" {
		t.Fatalf("persisted status after lease = %q, want running", got.Status)
	}

	// Owner release with no terminal verdict reverts to ready (re-runnable), in the real DB.
	if rec := call(http.MethodPost, "/projects/p/agent/lease/release", `{"host_id":"davinci","task_id":"T-1"}`); rec.Code != http.StatusOK {
		t.Fatalf("release: status=%d body=%s", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	if got, _ := store.GetTask(ctx, "T-1"); got.Status != "ready" {
		t.Fatalf("persisted status after owner release = %q, want ready", got.Status)
	}
}
