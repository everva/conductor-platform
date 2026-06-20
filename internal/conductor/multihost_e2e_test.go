//go:build e2e

// Package conductor e2e (multi-host capability routing, demo Faz-A): the
// DETERMINISTIC, cross-store proof that the platform routes work to the right
// HOST and drives MULTIPLE projects concurrently off ONE shared Postgres store —
// the two-machine (Linux + macOS) vision, proven offline.
//
// What is REAL here (no stubs on the load-bearing path):
//   - a SHARED real Postgres StateStore (an isolated schema on TEST_DATABASE_URL):
//     the host registry, the task ledger, AND the host-spanning lease table — the
//     exact surface two real daemons on two machines coordinate through;
//   - TWO independent Conductors against that ONE store, each built with a DISTINCT
//     capability set + host id (host-linux: linux,web,backend / host-mac: macos,
//     ios-build,maestro) — standing in for the two machines. The cross-host
//     coordination surface (lease table + capability-routed PickReady) is the store,
//     so in-process vs cross-process does not change the routing/lease semantics this
//     proves; the genuinely-separate-process run is the live demo (deploy/demo-2host);
//   - TWO throwaway product repos (web-proj, ios-proj), each cloned + worktreed for
//     real by the provisioner, gated by the real verify (go build/test), merged by
//     the real GitMerger with a [task:<id>] trailer;
//   - the deterministic sh-performer IN PLACE OF `claude -p` (the brain is swapped
//     for a script; everything else is the genuine article — same as e2e_test.go).
//
// The KANIT (proof), all on the shared PG store:
//  1. ROUTING SAFETY (negative): host-mac CANNOT pick web-proj's web task
//     (web ⊄ mac caps) and host-linux CANNOT pick ios-proj's ios task
//     (ios-build ⊄ linux caps) — each is a clean no-op, nothing merges.
//  2. ROUTING CORRECT (positive): host-linux merges the web task; host-mac merges
//     the ios task — MULTI-PROJECT, each on its capable host.
//  3. GATE AUTHORITY (negative): a lying performer on the routed host is BLOCKED by
//     the independent gate, never fake-greened (Rule#9), even in the multi-host wiring.
//
// Run (needs a real Postgres + git + go):
//
//	TEST_DATABASE_URL=postgres://conductor:conductor@localhost:5433/conductor?sslmode=disable \
//	  go test -tags e2e -run MultiHost -v ./internal/conductor/
package conductor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/provisioner"
	"github.com/everva/conductor-platform/internal/registry"
	"github.com/everva/conductor-platform/internal/statestore"
	"github.com/everva/conductor-platform/internal/verify"
	"github.com/jackc/pgx/v5/pgxpool"
)

// host capability sets standing in for the two machines (ADR-0008/0024). Web work
// is tagged requires:[web] so it routes to the Linux host (its caps include web);
// iOS work is tagged requires:[ios-build] so it routes to the Mac host. The web
// recipe carries no hard capability by design (web runs anywhere), so the demo tags
// the web TASK explicitly — exactly the field (Task.Requires) the router checks.
var (
	linuxCaps = []string{"linux", "web", "backend"}
	macCaps   = []string{"macos", "ios-build", "maestro"}
)

// TestE2E_MultiHostCapabilityRouting_TwoProjects proves capability routing +
// multi-project + the gate-authority across two hosts on one shared Postgres store.
func TestE2E_MultiHostCapabilityRouting_TwoProjects(t *testing.T) {
	requireGit(t)
	requireGo(t)
	ctx := context.Background()

	store, drop := freshConductorPGStore(t, ctx)
	defer drop()

	// --- two throwaway product repos: web-proj and ios-proj --------------------
	webRepo := newProductRepo(t)
	iosRepo := newProductRepo(t)

	const (
		webProj = "web-proj"
		iosProj = "ios-proj"
	)
	createProject(t, store, statestore.Project{ID: webProj, Repo: webRepo, BaseBranch: "develop"})
	createProject(t, store, statestore.Project{ID: iosProj, Repo: iosRepo, BaseBranch: "develop"})

	// --- register the two hosts (ADR-0024 agent-per-host) on the shared store ---
	// Routing keys off the Registry's capability set (WithCapabilities), but we also
	// record the hosts so the shared registry reflects the real two-machine topology.
	registerHost(t, store, statestore.Host{ID: "host-linux", Capabilities: linuxCaps})
	registerHost(t, store, statestore.Host{ID: "host-mac", Capabilities: macCaps})

	// --- the two routed tasks (explicit Requires = the router's input) ----------
	createScenario(t, store, statestore.Scenario{ID: "scn-web", ProjectID: webProj, Title: "web work", HoldoutRef: "noop"})
	createScenario(t, store, statestore.Scenario{ID: "scn-ios", ProjectID: iosProj, Title: "ios work", HoldoutRef: "noop"})
	createTask(t, store, statestore.Task{
		ID: "T-web", ProjectID: webProj, Lane: "web", Tier: "T2",
		Status: registry.StatusReady, ScenarioID: "scn-web", Requires: []string{"web"},
	})
	createTask(t, store, statestore.Task{
		ID: "T-ios", ProjectID: iosProj, Lane: "ios", Tier: "T2",
		Status: registry.StatusReady, ScenarioID: "scn-ios", Requires: []string{"ios-build"},
	})

	// --- the two "machines": distinct caps + host id, ONE shared store ----------
	good := writePerformer(t, goodPerformerScript)
	hostLinux := buildHostConductor(t, store, t.TempDir(), good, "host-linux", linuxCaps)
	hostMac := buildHostConductor(t, store, t.TempDir(), good, "host-mac", macCaps)

	// === 1. ROUTING SAFETY (negative): the wrong host CANNOT pick the task =======
	// host-mac ticking web-proj: web ⊄ {macos,ios-build,maestro} -> PickReady skips
	// it -> clean no-op. Nothing develops, nothing merges.
	if res, err := hostMac.Tick(ctx, webProj); err != nil || res.Outcome != OutcomeNoOp {
		t.Fatalf("routing-safety: host-mac on web-proj want no-op/nil, got outcome=%q err=%v", res.Outcome, err)
	}
	// host-linux ticking ios-proj: ios-build ⊄ {linux,web,backend} -> no-op.
	if res, err := hostLinux.Tick(ctx, iosProj); err != nil || res.Outcome != OutcomeNoOp {
		t.Fatalf("routing-safety: host-linux on ios-proj want no-op/nil, got outcome=%q err=%v", res.Outcome, err)
	}
	// Neither task moved off ready, and neither base branch advanced.
	assertTaskStatus(t, store, "T-web", registry.StatusReady)
	assertTaskStatus(t, store, "T-ios", registry.StatusReady)

	// === 2. ROUTING CORRECT (positive): the capable host drives each project =====
	resW, err := hostLinux.Tick(ctx, webProj)
	if err != nil || resW.Outcome != OutcomeMerged || resW.MergeSHA == "" {
		t.Fatalf("routing: host-linux on web-proj want merged, got outcome=%q sha=%q err=%v", resW.Outcome, resW.MergeSHA, err)
	}
	resI, err := hostMac.Tick(ctx, iosProj)
	if err != nil || resI.Outcome != OutcomeMerged || resI.MergeSHA == "" {
		t.Fatalf("routing: host-mac on ios-proj want merged, got outcome=%q sha=%q err=%v", resI.Outcome, resI.MergeSHA, err)
	}

	// statestore truth: each task is done, on its OWN project.
	assertTaskStatus(t, store, "T-web", registry.StatusDone)
	assertTaskStatus(t, store, "T-ios", registry.StatusDone)

	// git truth: each project's develop tip carries ITS task trailer + the new file,
	// and NOT the other project's trailer (no cross-contamination).
	webTip := gitT(t, filepath.Join(webRootClone(hostLinux, webProj)), "log", "-1", "--format=%B", "develop")
	if !strings.Contains(webTip, "[task:T-web]") || strings.Contains(webTip, "[task:T-ios]") {
		t.Fatalf("web-proj develop tip routing wrong; got:\n%s", webTip)
	}
	iosTip := gitT(t, filepath.Join(webRootClone(hostMac, iosProj)), "log", "-1", "--format=%B", "develop")
	if !strings.Contains(iosTip, "[task:T-ios]") || strings.Contains(iosTip, "[task:T-web]") {
		t.Fatalf("ios-proj develop tip routing wrong; got:\n%s", iosTip)
	}

	// === 3. GATE AUTHORITY on the routed host (negative, Rule#9) =================
	// A lying performer (self-reports pass, writes code that does NOT compile) on a
	// new web task: host-linux IS capable of it (web ⊆ caps), so routing lets it
	// through — but the INDEPENDENT gate must block it, never fake-green.
	createScenario(t, store, statestore.Scenario{ID: "scn-web-bad", ProjectID: webProj, Title: "lying web work", HoldoutRef: "noop"})
	createTask(t, store, statestore.Task{
		ID: "T-web-bad", ProjectID: webProj, Lane: "web", Tier: "T2",
		Status: registry.StatusReady, ScenarioID: "scn-web-bad", Requires: []string{"web"},
	})
	bad := writePerformer(t, badPerformerScript)
	hostLinuxBad := buildHostConductor(t, store, hostRoot(hostLinux), bad, "host-linux", linuxCaps)

	var last TickResult
	for i := 0; i <= MaxRetries+2; i++ {
		last, err = hostLinuxBad.Tick(ctx, webProj)
		if err != nil {
			t.Fatalf("gate-authority: tick %d errored: %v", i, err)
		}
		if last.Outcome == OutcomeMerged {
			t.Fatalf("gate-authority: a non-compiling task MERGED on host-linux (Rule#9 violated): %+v", last)
		}
		if last.Outcome == OutcomeBlocked {
			break
		}
		if last.Outcome != OutcomeRetry {
			t.Fatalf("gate-authority: tick %d unexpected outcome %q (want retry/blocked)", i, last.Outcome)
		}
	}
	if last.Outcome != OutcomeBlocked {
		t.Fatalf("gate-authority: lying task never reached blocked (last %q)", last.Outcome)
	}
	assertTaskStatus(t, store, "T-web-bad", registry.StatusBlocked)
	// No fake-green: web-proj develop tip is STILL T-web, never the blocked task.
	tip := gitT(t, filepath.Join(webRootClone(hostLinux, webProj)), "log", "-1", "--format=%B", "develop")
	if strings.Contains(tip, "[task:T-web-bad]") {
		t.Fatalf("gate-authority: blocked task's trailer reached develop (fake-green):\n%s", tip)
	}

	t.Logf("MULTI-HOST: routing-safety(mac⊄web, linux⊄ios)=no-op | routed: web→host-linux merged, ios→host-mac merged | gate-authority: lying web task BLOCKED on host-linux, no fake-green")
}

// --- wiring helpers (interface-store variants of the e2e_test.go helpers) -------

// buildHostConductor wires a Conductor against the SHARED StateStore with THIS
// host's capability-routed Picker + host id — the per-machine daemon the demo runs.
// It mirrors buildConductor but takes the interface store, a host id, and a
// capability set (so PickReady routes by Task.Requires ⊆ caps), and records the
// root on the merger's clone-path closure so a test can locate the clone.
func buildHostConductor(t *testing.T, store statestore.StateStore, root, performer, hostID string, caps []string) *Conductor {
	t.Helper()
	prov, err := provisioner.New(provisioner.Config{RootDir: root})
	if err != nil {
		t.Fatalf("provisioner.New: %v", err)
	}
	eng := engine.NewCommandEngine(engine.RecipeConfig{DevelopCmd: []string{performer}, Timeout: 60 * time.Second})
	verf := verify.New(noopHoldout{}, verify.Config{HoldoutCmd: []string{"true"}})
	merger := NewGitMerger(func(projectID string) string { return filepath.Join(root, "clones", projectID) })

	cond, err := New(Deps{
		Store:       store,
		Picker:      registry.NewRegistry(store, registry.WithCapabilities(caps)),
		Provisioner: prov,
		Engine:      eng,
		Verifier:    verf,
		Merger:      merger,
		Recipe: Recipe{Gates: []verify.Gate{
			{Name: "go build", Argv: []string{"go", "build", "./..."}},
			{Name: "go test", Argv: []string{"go", "test", "./..."}},
		}},
		HostID: hostID,
	})
	if err != nil {
		t.Fatalf("conductor.New(%s): %v", hostID, err)
	}
	hostRoots[cond] = root
	return cond
}

// hostRoots records each test Conductor's provisioner root so webRootClone/hostRoot
// can resolve the clone directory without threading the path through every call.
var hostRoots = map[*Conductor]string{}

func hostRoot(c *Conductor) string { return hostRoots[c] }

// webRootClone returns the clone directory a host laid the project's repo out under
// (rootDir/clones/<projectID>), matching the merger's clone-path closure.
func webRootClone(c *Conductor, projectID string) string {
	return filepath.Join(hostRoots[c], "clones", projectID)
}

// --- shared-store fixture helpers (interface, not *MemoryStore) -----------------

func createProject(t *testing.T, s statestore.StateStore, p statestore.Project) {
	t.Helper()
	if err := s.CreateProject(context.Background(), p); err != nil {
		t.Fatalf("create project %q: %v", p.ID, err)
	}
}

func createScenario(t *testing.T, s statestore.StateStore, sc statestore.Scenario) {
	t.Helper()
	if err := s.CreateScenario(context.Background(), sc); err != nil {
		t.Fatalf("create scenario %q: %v", sc.ID, err)
	}
}

func createTask(t *testing.T, s statestore.StateStore, task statestore.Task) {
	t.Helper()
	if err := s.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("create task %q: %v", task.ID, err)
	}
}

func registerHost(t *testing.T, s statestore.StateStore, h statestore.Host) {
	t.Helper()
	if err := s.RegisterHost(context.Background(), h); err != nil {
		t.Fatalf("register host %q: %v", h.ID, err)
	}
}

func assertTaskStatus(t *testing.T, s statestore.StateStore, id, want string) {
	t.Helper()
	got, err := s.GetTask(context.Background(), id)
	if err != nil {
		t.Fatalf("get task %q: %v", id, err)
	}
	if got.Status != want {
		t.Fatalf("task %q status = %q, want %q", id, got.Status, want)
	}
}

// --- real-Postgres harness (isolated schema, like cmd/conductor-api) ------------

var conductorPGSchemaCounter atomic.Int64

// freshConductorPGStore builds a shared PostgresStore on a FRESH isolated schema of
// TEST_DATABASE_URL (skipping cleanly when unset), migrates it, and returns the store
// + a drop cleanup. The isolated schema lets this run alongside the other real-PG
// tests without colliding on the shared lease/host/task tables — the whole point of
// the proof is that ONE store coordinates the two hosts, so the schema is shared
// between them and dropped at the end.
func freshConductorPGStore(t *testing.T, ctx context.Context) (statestore.StateStore, func()) {
	t.Helper()
	baseDSN := requirePGDSN(t)

	schema := fmt.Sprintf("cp_multihost_%d_%d", time.Now().UnixNano(), conductorPGSchemaCounter.Add(1))
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
	schemaDSN := baseDSN + sep + "search_path=" + schema

	store, err := statestore.NewPostgresStore(ctx, schemaDSN)
	if err != nil {
		t.Fatalf("NewPostgresStore on %s: %v", schema, err)
	}
	if err := store.Migrate(ctx); err != nil {
		store.Close()
		t.Fatalf("migrate schema %s: %v", schema, err)
	}

	drop := func() {
		store.Close()
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
	return store, drop
}

// requirePGDSN returns TEST_DATABASE_URL or skips — the real-PG gate (mirrors
// internal/statestore + cmd/conductor-api). Defined here under the e2e tag so the
// multi-host proof shares one DSN convention with the rest of the suite.
func requirePGDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping multi-host Postgres e2e")
	}
	return dsn
}
