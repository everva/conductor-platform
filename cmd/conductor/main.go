// Command conductor is the Faz-1a runnable daemon (ADR-0001): the process that
// actually DRIVES the conductor.Tick orchestration in production. conductorctl is
// the operator client that mutates the ledger; this binary is the worker that
// picks ready tasks off that same frozen statestore.StateStore and runs them
// through develop -> verify -> merge, one repo-scoped task at a time.
//
// It wires the SAME components the conductor e2e test wires — the in-memory
// statestore (Faz-1a; Postgres is a later task), the registry Picker, the real
// provisioner, the product CommandEngine, the independent verify gate, and the
// GitMerger — and then runs Tick either once (-once, for CI) or on a fixed
// interval until it is asked to stop. SIGINT/SIGTERM cancel the loop's context so
// an in-flight tick finishes and the process exits cleanly.
//
// It carries NO secret: the develop command is an operator-supplied command
// string (default a subscription `claude -p` style invocation), and no API key or
// token is ever read or embedded here. The recipe gates default to the same
// go build / go test the e2e test uses.
//
// It uses only the standard library: flag for config (each flag falls back to an
// env var), log/slog for structured per-tick logging, and os/signal for graceful
// shutdown. Exit codes are meaningful: 0 on a clean run/shutdown, non-zero on a
// configuration or tick error (the latter matters most for -once in CI).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/everva/conductor-platform/internal/conductor"
	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/events"
	"github.com/everva/conductor-platform/internal/governance"
	"github.com/everva/conductor-platform/internal/governor"
	"github.com/everva/conductor-platform/internal/heartbeat"
	"github.com/everva/conductor-platform/internal/holdout"
	"github.com/everva/conductor-platform/internal/provisioner"
	"github.com/everva/conductor-platform/internal/reconcile"
	"github.com/everva/conductor-platform/internal/registry"
	"github.com/everva/conductor-platform/internal/scaffolder"
	"github.com/everva/conductor-platform/internal/statestore"
	"github.com/everva/conductor-platform/internal/verify"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// defaultBaseBranch is the integration branch tasks are merged into when no
	// -base / CONDUCTOR_BASE_BRANCH is supplied (ADR-0004).
	defaultBaseBranch = "develop"
	// defaultInterval is the gap between ticks in loop mode.
	defaultInterval = 30 * time.Second
	// defaultHeartbeatInterval is the cadence of the background host-registry
	// heartbeat (C-1). At 30s it is well within the reconcile reaper's host-stale
	// threshold (CONDUCTOR_HOST_STALE, 2m default), so a live host running a long
	// develop (up to -timeout 30m) is refreshed ~every 30s and never false-reaped.
	// It is decoupled from the tick interval so a long develop (which blocks the
	// tick) cannot starve the heartbeat.
	defaultHeartbeatInterval = 30 * time.Second
	// defaultTimeout bounds a single develop subprocess so a hung performer cannot
	// stall a tick forever (the engine enforces this via ctx).
	defaultTimeout = 30 * time.Minute
	// defaultDevelopCmd is the subscription `claude -p` style performer the engine
	// runs in the worktree. It carries NO key — the subscription session does. It
	// is split into an argv with quote-aware tokenization (no shell expansion).
	// Override with -develop-cmd.
	defaultDevelopCmd = "claude -p"
	// defaultHoldoutCmd is the argv used to run the injected hidden holdout suite
	// inside the verify-worktree when a -holdout-store is configured (ADR-0018). It
	// runs the project's full Go test suite over the reviewed code with the holdout
	// files injected; it is split into an argv with quote-aware tokenization (no
	// shell expansion). Override with -holdout-cmd. It is unused when no holdout
	// store is configured.
	defaultHoldoutCmd = "go test ./..."
	// defaultPushRemote is the git remote the opt-in post-merge push targets when
	// -push is set without -push-remote (ADR-0022). Override with -push-remote /
	// CONDUCTOR_PUSH_REMOTE.
	defaultPushRemote = "origin"
	// defaultHeartbeatStale is the age past which the independent stall-detector
	// (-check) considers the heartbeat STALE. Generous relative to the default
	// 30s tick interval so a single slow tick does not false-alert; an external
	// alert job overrides via -heartbeat-stale / CONDUCTOR_HEARTBEAT_STALE.
	defaultHeartbeatStale = 5 * time.Minute
	// defaultLeaseTTL is the age past which the INDEPENDENT reconcile job (-reconcile)
	// reaps a stale lease via the TTL backstop (ADR-0016, second seam alongside the
	// host-heartbeat OwnerLive). Generous relative to a long-running develop so a
	// healthy in-flight task is never reaped; override via -lease-ttl / CONDUCTOR_LEASE_TTL.
	defaultLeaseTTL = 30 * time.Minute
	// defaultHostStale is the host-heartbeat age past which the reconcile job's
	// host-heartbeat OwnerLive (ADR-0024, 2B-3) treats the owning host as DEAD and
	// its lease reapable. Several heartbeat intervals so a single missed beat does
	// not free a live host's repo; override via -host-stale / CONDUCTOR_HOST_STALE.
	defaultHostStale = 2 * time.Minute
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	os.Exit(run(context.Background(), os.Args[1:], logger, os.Stderr))
}

// config is the resolved daemon configuration, parsed from flags with env
// fallback. It is a plain struct so run can build a Daemon from it and tests can
// construct one directly.
type config struct {
	// project is the project ID the daemon ticks. Required (no implicit default).
	project string
	// dsn is the Postgres connection string. Empty (default) selects the in-memory
	// StateStore (dev/test behavior, unchanged); non-empty selects the shared
	// Postgres StateStore (migrated on startup) so state persists across restarts
	// and is shared across hosts (ADR-0010, ADR-0013). It is NEVER logged.
	dsn string
	// rootDir is the workspace root the provisioner clones/worktrees under.
	rootDir string
	// baseBranch is the integration branch merges land on (project-level default).
	baseBranch string
	// interval is the gap between ticks in loop mode.
	interval time.Duration
	// timeout bounds a single develop subprocess.
	timeout time.Duration
	// developCmd is the performer command argv (program + args), no shell.
	developCmd []string
	// once runs a single tick and exits instead of looping.
	once bool
	// hostID identifies this host on the leases it acquires.
	hostID string
	// capabilities lists what this host can run (ADR-0024 agent-per-host): the
	// daemon self-registers it in the host registry on startup so capability-routing
	// (2B-2) can route a lane to this host only when lane.requires ⊆ capabilities
	// (ADR-0008). Parsed from -capabilities / CONDUCTOR_CAPABILITIES (comma-separated,
	// e.g. linux,backend,web or ios-build,macos,web). Empty = no capabilities (the
	// host still registers; it matches only no-requires lanes — backward compatible).
	capabilities []string
	// globalCap is the governor's max concurrent tasks across all projects
	// (ADR-0008). The lease table is the live count; this caps it.
	globalCap int
	// loadCeiling is the governor's normalized-load ceiling (loadavg / NumCPU)
	// above which new work is denied admission (ADR-0008 "host yük tavanı").
	loadCeiling float64
	// hostCap is the governor's PER-HOST concurrent-task cap (multi-host,
	// agent-per-host, ADR-0024): the max tasks THIS host runs simultaneously,
	// independent of the global cap across all hosts. 0 (the DEFAULT) = unlimited
	// per host = the per-host cap is disabled (only global + repo + load apply,
	// pre-2B-4 behavior). The governor counts this host's own active leases (by
	// hostID) against it.
	hostCap int
	// heartbeatPath is where the daemon writes its liveness heartbeat each tick
	// (ADR-0016). Empty = no heartbeat written (non-breaking no-op).
	heartbeatPath string
	// heartbeatStale is the staleness threshold the independent -check detector
	// uses to classify the heartbeat FRESH vs STALE.
	heartbeatStale time.Duration
	// heartbeatProgress, when > 0, enables progress-aware (no-progress) detection
	// in -check: a fresh heartbeat whose tick count never advances for longer than
	// this window is reported STALE (stuck). It is consulted only across repeated
	// checks in a long-lived detector process, so a single -check pass ignores it.
	heartbeatProgress time.Duration
	// check, when true, runs the INDEPENDENT stall-detector instead of the daemon:
	// read the heartbeat at heartbeatPath, print status, exit 0 FRESH / non-zero
	// STALE|MISSING (ADR-0016 independent backstop).
	check bool
	// reconcile, when true, runs the INDEPENDENT recovery job instead of the daemon
	// (ADR-0016): a conductor cannot rescue its own death, so the reaper runs as a
	// SEPARATE process (a k8s CronJob), never as a goroutine inside the daemon's tick
	// loop. One pass reaps stale/dead-host leases (ReapLeases) and derives merged
	// tasks done from git trailers (ReconcileTasks). It REQUIRES -dsn (the shared
	// Postgres store): reconciling across hosts needs the central store, never a
	// process-local memory store. It short-circuits before daemon wiring.
	reconcile bool
	// leaseTTL is the age past which -reconcile reaps a stale lease (TTL backstop,
	// ADR-0016). It is the reconcile.Config.LeaseTTL.
	leaseTTL time.Duration
	// hostStale is the host-heartbeat age past which -reconcile's host-heartbeat
	// OwnerLive treats the owning host as dead and its lease reapable (ADR-0024, 2B-3).
	hostStale time.Duration
	// httpAddr is the listen address for the OPTIONAL health/observability HTTP
	// server (k8s-style probes: /healthz /readyz /status). Empty (the DEFAULT)
	// DISABLES the server entirely, so the daemon's behavior is unchanged unless an
	// operator opts in (e.g. ":8080"). It carries no secret.
	httpAddr string
	// holdoutStore is the repo-EXTERNAL root directory the filesystem HoldoutStore
	// resolves hidden holdouts under (ADR-0018). Empty (the DEFAULT) keeps the inert
	// noop holdout so the daemon's behavior is unchanged (backward compatible); a
	// non-empty path wires holdout.FSStore so each scenario's HoldoutRef is fetched
	// and injected into the verify-worktree. The root path is operator config (logged
	// for diagnosis), not a secret; holdout file CONTENTS are never logged.
	holdoutStore string
	// holdoutCmd is the argv that runs the injected holdout suite inside the
	// verify-worktree (space-split, no shell). It is only used when holdoutStore is
	// set; it defaults to `go test ./...` so a configured holdout runs the project's
	// hidden test suite against the reviewed code.
	holdoutCmd []string
	// holdoutPrivateCache is the cache dir the private: holdout backing clones
	// private holdout repos under (ADR-0017/0018, Faz-1.5-a). Empty (the DEFAULT)
	// leaves the private: scheme UNconfigured — a private: locator then fails with a
	// clear "scheme not configured" error rather than a silent skip. The path is
	// operator config (logged for diagnosis), not a secret.
	holdoutPrivateCache string
	// holdoutGHToken is the gh-token the private: backing's credential helper uses
	// for private HTTPS auth (ADR-0017; deploy-keys forbidden). It is read from env
	// (CONDUCTOR_GH_TOKEN, falling back to GH_TOKEN) and is NEVER logged or
	// committed; private: errors redact it. Empty disables the credential helper
	// (e.g. a public/local holdout repo).
	holdoutGHToken string
	// recipeDir, when set, is the repo directory whose `.conductor/config.yaml`
	// (emitted by the scaffolder, ADR-0009) the daemon reads the verify gate recipe
	// from. Empty (the DEFAULT) uses the built-in default gates (go build + go test +
	// go vet) so a config-less project keeps working unchanged. A configured recipe
	// can opt INTO golangci-lint; if it does, golangci-lint MUST be installed or the
	// gate fails deterministically (never silently skipped).
	recipeDir string
	// push, when true, enables the opt-in post-merge remote-push (ADR-0022): after a
	// successful squash-merge the daemon pushes the advanced base branch to pushRemote.
	// Default false = LOCAL-ONLY merge (the Faz-1 behavior; no remote contact), so a
	// daemon that does not opt in never writes to a real remote. A push failure keeps
	// the task done and surfaces a push-failed event (never silent-loss, never undo).
	push bool
	// pushRemote is the git remote the opt-in push targets (ADR-0022). Default
	// "origin". Only meaningful when push is true. It is operator config, not a secret.
	pushRemote string
	// governance, when true (the DEFAULT), wires the risk-layered merge policy
	// (governance.DefaultPolicy, ADR-0003/N-10) into the conductor so high-tier
	// (T3/T4) and untiered tasks are HELD for a human after a green gate instead of
	// auto-merging; T1/T2 still auto-merge. When false, no policy is injected (nil)
	// so every green task auto-merges (the pre-N-10 behavior). Default true so
	// production correctly human-gates high-risk merges.
	governance bool
	// sentinel, when true, wires the 3-layer liveness progress-watchdog (ADR-0006,
	// Faz-2 2C-1) into the conductor: during develop a watcher periodically Assesses
	// liveness signals (engine Health + elapsed wall time) and, in the gray zone
	// (alive but output stalled past the grace window), consults a gray-zone advisor;
	// a Kill/Escalate cancels develop (killing the performer process group) and
	// blocks the task. The Layer-3 deterministic backstop (sentinelMaxTotal) ALWAYS
	// overrides the advisor. Default false = no watchdog (Layer-1+3-only via the
	// per-develop -timeout, the pre-2C-1 behavior). The gray-zone LLM advisor is wired
	// only when sentinelAdvisor is also true (and behind CP_REAL_CLAUDE); otherwise
	// the watchdog runs deterministic Layers 1+3 only.
	sentinel bool
	// sentinelMaxTotal is the Layer-3 absolute ceiling: a develop running longer than
	// this is killed regardless of any advisor verdict (the DF-difference). 0 = use
	// the per-develop -timeout as the only ceiling (the watchdog's backstop disabled,
	// process timeout still bounds the run). Only meaningful when sentinel is true.
	sentinelMaxTotal time.Duration
	// sentinelGrace is how long a develop must be stalled (no fresh activity) while
	// still alive before the gray-zone advisor is consulted (ADR-0006 Katman-2). 0
	// uses the package default. Only meaningful when sentinel is true.
	sentinelGrace time.Duration
	// sentinelAdvisor, when true, wires the gray-zone LLM advisor (real `claude -p`
	// behind CP_REAL_CLAUDE=1; ADR-0006 Katman-2). Default false = deterministic
	// Layers 1+3 only (no LLM). Only meaningful when sentinel is true.
	sentinelAdvisor bool
}

// run parses argv, builds the daemon, and drives it once or in a loop. It is
// split from main so it is unit-testable: tests pass argv + a logger and assert
// the exit code without spawning a process. It returns the process exit code.
func run(ctx context.Context, argv []string, logger *slog.Logger, stderr io.Writer) int {
	cfg, err := parseConfig(argv, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		_, _ = fmt.Fprintf(stderr, "conductor: %v\n", err)
		return 2
	}

	// Independent stall-detector mode (ADR-0016): read the heartbeat and exit on
	// its liveness verdict. This is the OUT-OF-PROCESS backstop an external
	// launchd/cron job invokes; it never builds or runs the daemon.
	if cfg.check {
		return runCheck(cfg, logger, stderr)
	}

	// Independent recovery job mode (ADR-0016): a conductor cannot rescue its own
	// death, so the stale-lease reaper + git-trailer task reconcile run as a SEPARATE
	// process (a k8s CronJob), never as a goroutine inside the daemon's tick loop.
	// One pass against the shared store, then exit (0 ok / non-zero error).
	if cfg.reconcile {
		return runReconcile(ctx, cfg, logger, stderr)
	}

	d, err := newDaemon(cfg, logger)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "conductor: %v\n", err)
		return 1
	}
	// Release backend resources (the Postgres pgxpool) on exit. No-op for memory.
	defer d.Close()

	// Trap SIGINT/SIGTERM: cancelling this context unwinds the loop after the
	// in-flight tick finishes (the tick's own ctx is derived from it, so a
	// long-running develop is also cancelled — the engine maps that to ErrNoVerdict,
	// not a fake-green).
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := d.Run(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "conductor: %v\n", err)
		return 1
	}
	return 0
}

// runCheck is the INDEPENDENT stall-detector entry point (ADR-0016): it reads
// the heartbeat at cfg.heartbeatPath and exits on its liveness verdict, never
// touching the daemon's wiring (store/engine/git). An external launchd/cron job
// runs `conductor -check -heartbeat <path>` periodically and alerts on the
// non-zero exit. The default Notifier logs the stall; the exit code is the
// machine-readable signal (see heartbeat.Detect for the code contract).
func runCheck(cfg config, logger *slog.Logger, stderr io.Writer) int {
	chk, err := heartbeat.New(cfg.heartbeatPath, heartbeat.CheckerConfig{Threshold: cfg.heartbeatStale})
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "conductor: %v\n", err)
		return 2
	}
	if cfg.heartbeatProgress > 0 {
		chk.WithProgressWindow(cfg.heartbeatProgress)
	}
	return heartbeat.Detect(chk, heartbeat.LogNotifier{Logger: logger}, stderr)
}

// runReconcile is the INDEPENDENT recovery-job entry point (ADR-0016): it opens
// the SHARED Postgres store from cfg.dsn, builds the deterministic Reconciler
// (TTL backstop + host-heartbeat OwnerLive + a real git-log reader for trailer
// reconcile), runs ONE pass (ReapLeases + ReconcileTasks across every project),
// then exits — 0 on success, non-zero on error, so a k8s CronJob fails-loud on a
// reconcile error. It is a SEPARATE process from the daemon (a conductor cannot
// rescue its own death), never a goroutine in the tick loop, and it builds NONE
// of the daemon's engine/verify/loop wiring. The DSN is never logged.
func runReconcile(ctx context.Context, cfg config, logger *slog.Logger, stderr io.Writer) int {
	store, closer, err := newStore(ctx, cfg, logger)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "conductor: %v\n", err)
		return 1
	}
	defer closer()

	// The git reader derives merged→done from EXACT [task:<id>] trailers on each
	// project's base branch (ADR-0004/0010), reading the per-project clone the daemon
	// merges into (rootDir/clones/<projectID>). When -root is unset the clones are not
	// reachable, so trailer reconcile finds no commits (a clean no-op) — the lease
	// reaper still runs fully, since it reads only the store. The reader shells out to
	// `git log` (no LLM), mirroring the merger's git invocation.
	gitReader := newCloneGitReader(cfg.rootDir)

	now := time.Now().UTC()
	if err := reconcileRun(ctx, store, gitReader, cfg, now, logger); err != nil {
		_, _ = fmt.Fprintf(stderr, "conductor: %v\n", err)
		return 1
	}
	return 0
}

// reconcileRun executes ONE deterministic reconcile pass over the given store and
// git reader (ADR-0016): it reaps stale/dead-host leases (ReapLeases) and derives
// merged tasks done from git trailers (ReconcileTasks) for EVERY project. `now` is
// injected so the pass is deterministic and unit-testable without os.Exit. It is
// split from runReconcile so a test can drive it against an in-memory or real-PG
// store with a fixed clock and a fake/real git reader. It logs the actions it takes
// (leases reaped, projects reconciled) and never logs any secret.
func reconcileRun(ctx context.Context, store statestore.StateStore, git reconcile.GitReader, cfg config, now time.Time, logger *slog.Logger) error {
	// OwnerLive is the cross-host liveness predicate (ADR-0024, 2B-3): a lease whose
	// owning host stopped heartbeating (older than cfg.hostStale) is reapable; a fresh
	// host's lease is kept. The TTL backstop (cfg.leaseTTL) is the independent second
	// seam. Both are deterministic in `now` (no hidden wall clock).
	ownerLive := reconcile.HostHeartbeatOwnerLive(ctx, store, cfg.hostStale, now)
	rec := reconcile.New(store, git, reconcile.Config{
		LeaseTTL:  cfg.leaseTTL,
		OwnerLive: ownerLive,
	})

	// Snapshot the live leases before the reap so the log can report WHICH leases the
	// pass freed (ReapLeases itself returns only an error). This read is on the frozen
	// interface; it is purely for observability and does not change the reap decision.
	before, err := store.ListLeases(ctx)
	if err != nil {
		return fmt.Errorf("reconcile: list leases (pre-reap): %w", err)
	}
	if err := rec.ReapLeases(ctx, now); err != nil {
		return err
	}
	after, err := store.ListLeases(ctx)
	if err != nil {
		return fmt.Errorf("reconcile: list leases (post-reap): %w", err)
	}
	stillHeld := make(map[string]struct{}, len(after))
	for _, l := range after {
		stillHeld[l.ProjectID] = struct{}{}
	}
	reaped := 0
	for _, l := range before {
		if _, held := stillHeld[l.ProjectID]; !held {
			reaped++
			logger.Info("reconcile: reaped stale lease (ADR-0016)",
				slog.String("project", l.ProjectID),
				slog.String("host", l.HostID),
				slog.String("task", l.TaskID),
				slog.Time("acquired_at", l.AcquiredAt))
		}
	}

	// Git-trailer task reconcile (ADR-0004/0010): for EVERY project, derive merged
	// tasks done from EXACT [task:<id>] trailers on the base branch. A project whose
	// clone is unreachable yields no commits (clean no-op). A per-project error is
	// fatal to the pass so the CronJob fails-loud rather than silently skipping work.
	projects, err := store.ListProjects(ctx)
	if err != nil {
		return fmt.Errorf("reconcile: list projects: %w", err)
	}
	for _, p := range projects {
		if err := rec.ReconcileTasks(ctx, p); err != nil {
			return err
		}
	}

	logger.Info("reconcile pass complete (ADR-0016 independent recovery job)",
		slog.Int("leases_reaped", reaped),
		slog.Int("leases_remaining", len(after)),
		slog.Int("projects_reconciled", len(projects)),
		slog.Duration("lease_ttl", cfg.leaseTTL),
		slog.Duration("host_stale", cfg.hostStale))
	return nil
}

// parseConfig parses argv into a config, applying env fallback per flag and
// validating the required fields. Flag values win over env; env wins over the
// built-in default.
func parseConfig(argv []string, stderr io.Writer) (config, error) {
	fs := flag.NewFlagSet("conductor", flag.ContinueOnError)
	fs.SetOutput(stderr)

	project := fs.String("project", envOr("CONDUCTOR_PROJECT", ""), "project id to tick (required)")
	dsn := fs.String("dsn", envOr("CONDUCTOR_DSN", ""),
		"Postgres connection string; empty = in-memory store (dev/test). Non-empty = shared Postgres store, migrated on start. Never logged")
	rootDir := fs.String("root", envOr("CONDUCTOR_ROOT", ""), "workspace root for clones/worktrees (required)")
	baseBranch := fs.String("base", envOr("CONDUCTOR_BASE_BRANCH", defaultBaseBranch), "integration base branch")
	developCmd := fs.String("develop-cmd", envOr("CONDUCTOR_DEVELOP_CMD", defaultDevelopCmd),
		`performer command run in the worktree (quote-aware argv, no shell expansion; e.g. claude -p "do the task"; subscription claude -p style, no key)`)
	hostID := fs.String("host", envOr("CONDUCTOR_HOST_ID", ""), "host id recorded on leases (default: hostname)")
	capabilities := fs.String("capabilities", envOr("CONDUCTOR_CAPABILITIES", ""),
		"comma-separated host capabilities self-registered in the host registry (ADR-0024), e.g. linux,backend,web or ios-build,macos,web; empty = no capabilities (matches only no-requires lanes)")
	interval := fs.Duration("interval", envDurationOr("CONDUCTOR_INTERVAL", defaultInterval), "gap between ticks in loop mode")
	timeout := fs.Duration("timeout", envDurationOr("CONDUCTOR_TIMEOUT", defaultTimeout), "per-develop subprocess timeout")
	once := fs.Bool("once", envBoolOr("CONDUCTOR_ONCE", false), "run a single tick and exit (exit 0 on success)")
	maxConc := fs.Int("max-concurrent", envIntOr("CONDUCTOR_MAX_CONCURRENT", 1), "max concurrent tasks (Faz-1a: must be 1)")
	globalCap := fs.Int("global-cap", envIntOr("CONDUCTOR_GLOBAL_CAP", governor.DefaultGlobalCap),
		"governor: max concurrent tasks across all projects (lease-derived; ADR-0008)")
	loadCeiling := fs.Float64("load-ceiling", envFloatOr("CONDUCTOR_LOAD_CEILING", governor.DefaultLoadCeiling),
		"governor: normalized 1-min load ceiling (loadavg/NumCPU) above which new work is denied")
	hostCap := fs.Int("host-cap", envIntOr("CONDUCTOR_HOST_CAP", 0),
		"governor: PER-HOST max concurrent tasks on THIS host (multi-host, ADR-0024; counts this host's own active leases). 0 (default) = unlimited per host = disabled")
	heartbeat := fs.String("heartbeat", envOr("CONDUCTOR_HEARTBEAT", ""),
		"path the daemon writes its liveness heartbeat to each tick (ADR-0016); empty = disabled")
	heartbeatStale := fs.Duration("heartbeat-stale", envDurationOr("CONDUCTOR_HEARTBEAT_STALE", defaultHeartbeatStale),
		"independent -check: heartbeat age past which the daemon is reported STALE")
	heartbeatProgress := fs.Duration("heartbeat-progress", envDurationOr("CONDUCTOR_HEARTBEAT_PROGRESS", 0),
		"independent -check: if >0, a fresh heartbeat whose tick never advances for this long is STALE (stuck)")
	check := fs.Bool("check", envBoolOr("CONDUCTOR_CHECK", false),
		"run the INDEPENDENT stall-detector over -heartbeat and exit (0=FRESH, non-zero=STALE/MISSING); does not start the daemon")
	reconcileMode := fs.Bool("reconcile", envBoolOr("CONDUCTOR_RECONCILE", false),
		"run the INDEPENDENT recovery job (ADR-0016): ONE pass of stale/dead-host lease reaping + git-trailer task reconcile over the shared -dsn store, then exit (0=ok, non-zero=error). Requires -dsn; does not start the daemon")
	leaseTTL := fs.Duration("lease-ttl", envDurationOr("CONDUCTOR_LEASE_TTL", defaultLeaseTTL),
		"independent -reconcile: lease age past which the TTL backstop reaps a stale lease (ADR-0016)")
	hostStale := fs.Duration("host-stale", envDurationOr("CONDUCTOR_HOST_STALE", defaultHostStale),
		"independent -reconcile: host-heartbeat age past which the owning host is treated DEAD and its lease reapable (ADR-0024, 2B-3)")
	httpAddr := fs.String("http-addr", envOr("CONDUCTOR_HTTP_ADDR", ""),
		"listen address for the OPTIONAL health HTTP server (/healthz /readyz /status), e.g. :8080; empty = disabled")
	governance := fs.Bool("governance", envBoolOr("CONDUCTOR_GOVERNANCE", true),
		"wire the risk-layered merge policy (ADR-0003): high-tier/untiered tasks are HELD for a human after a green gate; false = auto-merge all (default true)")
	sentinelOn := fs.Bool("sentinel", envBoolOr("CONDUCTOR_SENTINEL", false),
		"wire the 3-layer liveness progress-watchdog (ADR-0006, 2C-1): kill a stuck/dead develop, escalate needs-human, backstop ALWAYS overrides the gray-zone advisor; false = no watchdog (Layer-1+3-only via -timeout, default false)")
	sentinelMaxTotal := fs.Duration("sentinel-max-total", envDurationOr("CONDUCTOR_SENTINEL_MAX_TOTAL", 0),
		"sentinel Layer-3 absolute ceiling: a develop running longer is killed regardless of any advisor verdict (the DF-difference); 0 = use -timeout as the only ceiling. Only meaningful with -sentinel")
	sentinelGrace := fs.Duration("sentinel-grace", envDurationOr("CONDUCTOR_SENTINEL_GRACE", 0),
		"sentinel gray-zone grace: how long a develop is stalled-but-alive before the advisor is consulted (ADR-0006 Katman-2); 0 = package default. Only meaningful with -sentinel")
	sentinelAdvisor := fs.Bool("sentinel-advisor", envBoolOr("CONDUCTOR_SENTINEL_ADVISOR", false),
		"wire the gray-zone LLM advisor (real `claude -p` behind CP_REAL_CLAUDE=1; ADR-0006 Katman-2); false = deterministic Layers 1+3 only. Only meaningful with -sentinel")
	holdoutStore := fs.String("holdout-store", envOr("CONDUCTOR_HOLDOUT_STORE", ""),
		"repo-EXTERNAL root dir the hidden holdout (ADR-0018) is resolved under; empty = inert noop holdout (backward compatible). Path is logged, contents are not")
	holdoutCmd := fs.String("holdout-cmd", envOr("CONDUCTOR_HOLDOUT_CMD", defaultHoldoutCmd),
		"argv that runs the injected holdout suite in the verify-worktree (quote-aware, no shell expansion); used only when -holdout-store is set")
	recipeDir := fs.String("recipe-dir", envOr("CONDUCTOR_RECIPE_DIR", ""),
		"repo dir whose .conductor/config.yaml (ADR-0009) supplies the PER-PROJECT recipe: BOTH the develop command and the verify gates (2A-1). Empty = built-in default gates (go build + go test + go vet) + the -develop-cmd flag. A recipe's develop command overrides -develop-cmd unless it is empty/the inert placeholder; a recipe may opt into golangci-lint, which must then be installed or the gate fails")
	holdoutPrivateCache := fs.String("holdout-private-cache", envOr("CONDUCTOR_HOLDOUT_PRIVATE_CACHE", ""),
		"cache dir the private: holdout backing (ADR-0018) clones private holdout repos under; empty = private: scheme unconfigured (a private: locator then errors clearly). Path is logged, the gh-token is not")
	push := fs.Bool("push", envBoolOr("CONDUCTOR_PUSH", false),
		"opt-in (ADR-0022): after a successful squash-merge, push the base branch to -push-remote (gh-token from CONDUCTOR_GH_TOKEN/GH_TOKEN). Default false = local-only merge. A push failure keeps the task done and emits a push-failed event")
	pushRemote := fs.String("push-remote", envOr("CONDUCTOR_PUSH_REMOTE", defaultPushRemote),
		"git remote the opt-in post-merge push targets (ADR-0022); only used with -push")

	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "conductor — Faz-1a tick daemon")
		_, _ = fmt.Fprintln(stderr, "\nusage: conductor -project <id> -root <dir> [-once] [-interval 30s] [-develop-cmd \"claude -p\"]")
		_, _ = fmt.Fprintln(stderr, "       conductor -check -heartbeat <path>   # independent stall-detector (ADR-0016)")
		_, _ = fmt.Fprintln(stderr, "       conductor -reconcile -dsn <postgres> # independent recovery job: reap stale leases + reconcile tasks (ADR-0016)")
		fs.PrintDefaults()
	}

	if err := fs.Parse(argv); err != nil {
		return config{}, err
	}

	// -check is the INDEPENDENT stall-detector: it only reads the heartbeat, so it
	// needs none of the daemon's required fields (project/root). It must NOT build
	// the daemon, so validate just its own input and return early.
	if *check {
		if *heartbeat == "" {
			return config{}, errors.New("-check requires -heartbeat <path> (or set CONDUCTOR_HEARTBEAT)")
		}
		if *heartbeatStale <= 0 {
			return config{}, fmt.Errorf("-heartbeat-stale must be positive (got %s)", *heartbeatStale)
		}
		return config{
			check:             true,
			heartbeatPath:     *heartbeat,
			heartbeatStale:    *heartbeatStale,
			heartbeatProgress: *heartbeatProgress,
		}, nil
	}

	// -reconcile is the INDEPENDENT recovery job (ADR-0016): it reaps stale/dead-host
	// leases and reconciles git-trailer→done across ALL hosts, so it needs the SHARED
	// Postgres store, never a process-local memory store — a memory store would see no
	// other host's leases and reap nothing. It must NOT build the daemon, so validate
	// just its own input (a present -dsn, positive thresholds) and return early. The
	// host id (for logging/derivation parity) is resolved like the daemon's.
	if *reconcileMode {
		if *dsn == "" {
			return config{}, errors.New("-reconcile requires -dsn <postgres> (or set CONDUCTOR_DSN): reaping across hosts needs the shared central store, not an in-memory one")
		}
		if *leaseTTL <= 0 {
			return config{}, fmt.Errorf("-lease-ttl must be positive (got %s)", *leaseTTL)
		}
		if *hostStale <= 0 {
			return config{}, fmt.Errorf("-host-stale must be positive (got %s)", *hostStale)
		}
		host := *hostID
		if host == "" {
			if hn, err := os.Hostname(); err == nil {
				host = hn
			}
		}
		return config{
			reconcile:  true,
			dsn:        *dsn,
			hostID:     host,
			baseBranch: *baseBranch,
			rootDir:    *rootDir,
			leaseTTL:   *leaseTTL,
			hostStale:  *hostStale,
		}, nil
	}

	if *project == "" {
		return config{}, errors.New("-project is required (or set CONDUCTOR_PROJECT)")
	}
	if *rootDir == "" {
		return config{}, errors.New("-root is required (or set CONDUCTOR_ROOT)")
	}
	if *maxConc != 1 {
		return config{}, fmt.Errorf("-max-concurrent must be 1 in Faz-1a (got %d): one active task per repo (ADR-0008)", *maxConc)
	}
	if !*once && *interval <= 0 {
		return config{}, fmt.Errorf("-interval must be positive in loop mode (got %s)", *interval)
	}

	cmd, err := splitArgs(*developCmd)
	if err != nil {
		return config{}, fmt.Errorf("-develop-cmd: %w", err)
	}
	if len(cmd) == 0 {
		return config{}, errors.New("-develop-cmd must not be empty")
	}

	// The holdout suite argv is parsed regardless, but only meaningful when a
	// holdout store is configured. When the store IS set, an empty holdout command
	// would leave the verifier with nothing to run, so reject it loudly.
	hcmd, err := splitArgs(*holdoutCmd)
	if err != nil {
		return config{}, fmt.Errorf("-holdout-cmd: %w", err)
	}
	if *holdoutStore != "" && len(hcmd) == 0 {
		return config{}, errors.New("-holdout-cmd must not be empty when -holdout-store is set")
	}

	host := *hostID
	if host == "" {
		if hn, err := os.Hostname(); err == nil {
			host = hn
		}
	}

	return config{
		project:           *project,
		dsn:               *dsn,
		rootDir:           *rootDir,
		baseBranch:        *baseBranch,
		interval:          *interval,
		timeout:           *timeout,
		developCmd:        cmd,
		once:              *once,
		hostID:            host,
		capabilities:      splitCapabilities(*capabilities),
		globalCap:         *globalCap,
		loadCeiling:       *loadCeiling,
		hostCap:           *hostCap,
		heartbeatPath:     *heartbeat,
		heartbeatStale:    *heartbeatStale,
		heartbeatProgress: *heartbeatProgress,
		httpAddr:          *httpAddr,
		governance:        *governance,
		holdoutStore:      *holdoutStore,
		holdoutCmd:        hcmd,
		recipeDir:         *recipeDir,
		// private: holdout backing config. The cache dir is a flag (path, not a
		// secret); the gh-token is env-ONLY so it never appears in the process argv
		// (CONDUCTOR_GH_TOKEN, falling back to GH_TOKEN). It is never logged.
		holdoutPrivateCache: *holdoutPrivateCache,
		holdoutGHToken:      envOr("CONDUCTOR_GH_TOKEN", os.Getenv("GH_TOKEN")),
		// Opt-in remote-push (ADR-0022). The gh-token is reused from the env-only
		// holdout token (CONDUCTOR_GH_TOKEN / GH_TOKEN) so it never appears in argv.
		push:       *push,
		pushRemote: *pushRemote,
		// Sentinel 3-layer liveness watchdog (ADR-0006, 2C-1). Off by default; the
		// gray-zone LLM advisor is wired only when sentinelAdvisor is also set.
		sentinel:         *sentinelOn,
		sentinelMaxTotal: *sentinelMaxTotal,
		sentinelGrace:    *sentinelGrace,
		sentinelAdvisor:  *sentinelAdvisor,
	}, nil
}

// Daemon owns a wired Conductor and the loop policy (interval / once). It is the
// runnable core: construct with newDaemon, drive with Run. It is decoupled from
// flag parsing so a test can build one against an in-memory setup.
type Daemon struct {
	cond     *conductor.Conductor
	project  string
	interval time.Duration
	once     bool
	logger   *slog.Logger
	// store is the StateStore the daemon ticks against; it is retained so the tick
	// loop can advance this host's registry heartbeat (ADR-0024 agent-per-host).
	store statestore.StateStore
	// hostID is this host's registry id; a DEDICATED background goroutine advances
	// its LastHeartbeat on a fixed cadence (separate from the ADR-0016 FILE
	// heartbeat, which is liveness telemetry, not a PG registry row).
	hostID string
	// hbInterval is the cadence of the background host-registry heartbeat (C-1). It
	// is INDEPENDENT of develop duration: the heartbeat goroutine fires every
	// hbInterval regardless of how long a tick blocks, so a host running a long
	// (up to -timeout) develop stays fresh and is never false-reaped by the
	// reconcile job (HostHeartbeatOwnerLive, host-stale default 2m). Zero selects
	// defaultHeartbeatInterval.
	hbInterval time.Duration
	// hbClock is the injectable wall clock the heartbeat goroutine stamps with. Nil
	// uses time.Now; a test injects a controllable clock to prove the heartbeat
	// advances independent of tick progress without sleeping a real develop.
	hbClock func() time.Time
	// hb writes the liveness heartbeat each tick (ADR-0016). It is nil when no
	// -heartbeat path is configured; *heartbeat.Writer treats a nil receiver as a
	// no-op, so the daemon stays non-breaking without nil-guards at call sites.
	hb *heartbeat.Writer
	// ticks counts ticks attempted; it is the heartbeat's progress signal.
	ticks uint64
	// health is the OPTIONAL health/observability HTTP server (P4-2). It is nil
	// when no -http-addr is configured (the DEFAULT), in which case Run starts no
	// server and the daemon behaves exactly as before. When non-nil, Run starts it
	// in a goroutine and shuts it down on the same ctx cancel as the tick loop.
	health *healthServer
	// httpAddr is the resolved listen address for health; empty disables the server.
	httpAddr string
	// boundAddr records the ACTUAL address the health listener bound (resolving an
	// ephemeral ":0" to a real port). It is set once under boundMu when the server
	// binds and read by boundHealthAddr; it lets a test discover the OS-assigned
	// port without reaching into the serve goroutine.
	boundMu   sync.Mutex
	boundAddr string
	// snap is the concurrency-safe last-tick snapshot /status reads. It is updated
	// each tick. It is always non-nil so tick can record unconditionally.
	snap *tickSnapshot
	// closer releases backend resources on shutdown (the Postgres statestore pool
	// AND, for a Postgres event bus, its LISTENer connections + pool). It is nil
	// for the all-memory dev setup, which owns no external resource. Close invokes
	// it exactly once.
	closer func()
}

// Close releases any backend resources the daemon owns (the Postgres connection
// pool). It is a no-op for the in-memory store and is safe to call exactly once
// on shutdown. The caller (run) defers it so the pgxpool is always released.
func (d *Daemon) Close() {
	if d.closer != nil {
		d.closer()
	}
}

// newDaemon wires the SAME components the conductor e2e test wires (registry
// Picker, real provisioner, product CommandEngine, verify gate, GitMerger) into a
// Conductor and ensures the project row exists so a tick has something to resolve.
// The develop command is the operator-supplied performer; it carries no key.
//
// The StateStore backend is selected by cfg.dsn: empty selects the in-memory
// store (dev/test, unchanged), non-empty selects the shared Postgres store, which
// is migrated on startup so a fresh database is schema-ready. The returned
// Daemon's Close releases the Postgres pool; the caller must defer it.
func newDaemon(cfg config, logger *slog.Logger) (*Daemon, error) {
	store, closer, err := newStore(context.Background(), cfg, logger)
	if err != nil {
		return nil, err
	}

	// Ensure the project the daemon ticks exists. CreateProject is an upsert that
	// does ON CONFLICT DO NOTHING (returning ErrAlreadyExists), so a project already
	// onboarded via conductorctl is left untouched and a brand-new store gets a bare
	// project row to resolve against. PickReady reads tasks from the same store; a
	// project with no ready tasks cleanly no-ops (what the -once test asserts). For a
	// real Postgres DSN the project and its tasks are normally onboarded out-of-band
	// (conductorctl); this only guarantees the row is present so the tick never errors
	// on a missing project. A failure to release the Postgres pool here would leak it,
	// so close on any error path.
	if err := store.CreateProject(context.Background(), statestore.Project{
		ID:         cfg.project,
		BaseBranch: cfg.baseBranch,
	}); err != nil && !errors.Is(err, statestore.ErrAlreadyExists) {
		closer()
		return nil, fmt.Errorf("ensure project %q: %w", cfg.project, err)
	}

	// Self-registration in the host registry (ADR-0024 agent-per-host, 2B-1): the
	// daemon records its identity + capabilities so capability-routing (2B-2) and
	// multi-host coordination can read which hosts exist and what each can run. It
	// is an UPSERT (RegisterHost), so a restart refreshes capabilities + heartbeat
	// rather than failing. It runs for BOTH backends: for a shared Postgres store it
	// is the cross-host registry; for the in-memory single-process store it registers
	// into that process's own store (harmless, keeps the path uniform). Capabilities
	// are logged (operator config, not a secret); no token is touched.
	if err := store.RegisterHost(context.Background(), statestore.Host{
		ID:           cfg.hostID,
		Capabilities: cfg.capabilities,
	}); err != nil {
		closer()
		return nil, fmt.Errorf("register host %q: %w", cfg.hostID, err)
	}
	logger.Info("host registered (ADR-0024 agent-per-host)",
		slog.String("host", cfg.hostID),
		slog.String("capabilities", strings.Join(cfg.capabilities, ",")))

	prov, err := provisioner.New(provisioner.Config{RootDir: cfg.rootDir})
	if err != nil {
		closer()
		return nil, fmt.Errorf("provisioner: %w", err)
	}

	// Per-project recipe (2A-1): resolve BOTH the develop command and the verify
	// gates from the project's .conductor/config.yaml when a -recipe-dir is
	// configured and present; otherwise fall back to the global -develop-cmd flag +
	// default gates (backward compatible). Resolved before the engine so the engine
	// drives the project's OWN performer when the recipe declares one.
	developCmd, gates, err := resolveRecipe(cfg, logger)
	if err != nil {
		closer()
		return nil, fmt.Errorf("resolve recipe: %w", err)
	}

	eng := engine.NewCommandEngine(engine.RecipeConfig{
		DevelopCmd: developCmd,
		Timeout:    cfg.timeout,
	})

	verf, verfCloser, err := newVerifier(context.Background(), cfg, logger)
	if err != nil {
		closer()
		return nil, fmt.Errorf("verifier: %w", err)
	}
	// Compose: release the holdout PG pool (if the pg:// backing opened one) AND the
	// store on Close. verfCloser is always non-nil (a no-op when no pg pool).
	storeOnlyCloser := closer
	closer = func() {
		verfCloser()
		storeOnlyCloser()
	}

	// Opt-in remote-push (ADR-0022): default OFF = local-only merge. When enabled,
	// a successful squash-merge pushes the base to cfg.pushRemote via a gh-token
	// credential helper (reusing the env-only token); a push failure keeps the task
	// done and emits a push-failed event. The token is NEVER logged.
	merger := conductor.NewGitMerger(func(projectID string) string {
		return cfg.rootDir + "/clones/" + projectID
	}, conductor.WithPush(conductor.PushConfig{
		Enabled: cfg.push,
		Remote:  cfg.pushRemote,
		GHToken: cfg.holdoutGHToken,
	}))
	if cfg.push {
		logger.Info("remote-push enabled (ADR-0022)",
			slog.String("remote", cfg.pushRemote),
			slog.Bool("has_token", cfg.holdoutGHToken != ""))
	} else {
		logger.Info("remote-push disabled (ADR-0022): local-only merge", slog.Bool("push", false))
	}

	// Resource-governor (ADR-0008, N-5): admission control consulted before each
	// lease/develop. The global count is derived from the shared lease table
	// (ListLeases), never a parallel counter; host load is read through the
	// default system probe. Default config is permissive enough not to false-deny
	// on a normal dev machine.
	gov, err := governor.New(store, governor.SystemLoadProbe{}, governor.Config{
		GlobalCap:   cfg.globalCap,
		LoadCeiling: cfg.loadCeiling,
		// Per-host concurrent-task cap (multi-host, agent-per-host, ADR-0024, 2B-4):
		// the governor counts this host's OWN active leases (by hostID) against
		// hostCap. hostCap <= 0 leaves the per-host cap disabled (pre-2B-4 behavior).
		HostID:  cfg.hostID,
		HostCap: cfg.hostCap,
	})
	if err != nil {
		closer()
		return nil, fmt.Errorf("governor: %w", err)
	}
	if cfg.hostCap > 0 {
		logger.Info("governor per-host cap enabled (ADR-0024 agent-per-host)",
			slog.String("host", cfg.hostID),
			slog.Int("host_cap", cfg.hostCap),
			slog.Int("global_cap", cfg.globalCap))
	} else {
		logger.Info("governor per-host cap disabled (unlimited per host)",
			slog.Int("global_cap", cfg.globalCap))
	}

	// Observability bus (ADR-0011, N-9): the event Emitter the tick publishes
	// lifecycle events on. memory for the in-process dev setup, Postgres
	// LISTEN/NOTIFY when a DSN is set (so external subscribers see events). Its
	// closer is composed onto the store closer so both are released on shutdown.
	emitter, busCloser, err := newEmitter(context.Background(), cfg, logger)
	if err != nil {
		closer()
		return nil, fmt.Errorf("event bus: %w", err)
	}
	// Compose: release the event bus AND the store on Close, exactly once each.
	storeCloser := closer
	closer = func() {
		busCloser()
		storeCloser()
	}

	// Governance merge policy (ADR-0003, N-10): default ON so production human-gates
	// high-tier (T3/T4) and untiered tasks after a green gate; -governance=false
	// leaves it nil so every green task auto-merges (pre-N-10 behavior).
	policy := newPolicy(cfg, logger)

	cond, err := conductor.New(conductor.Deps{
		Store: store,
		// Capability-routing (ADR-0024 agent-per-host, ADR-0008, 2B-2): build the
		// Picker with THIS host's capabilities so PickReady picks a task only when
		// Task.Requires ⊆ capabilities, leaving tasks this host cannot run for a capable
		// host (pull-based). An empty capability set leaves routing UNCONSTRAINED (the
		// pre-2B-2 behavior), so a host that never declared -capabilities is unaffected.
		Picker:      registry.NewRegistry(store, registry.WithCapabilities(cfg.capabilities)),
		Provisioner: prov,
		Engine:      eng,
		Verifier:    verf,
		Merger:      merger,
		Recipe:      conductor.Recipe{Gates: gates},
		HostID:      cfg.hostID,
		Governor:    gov,
		Emitter:     emitter,
		Policy:      policy,
		// Control reverse-channel pause-gate (ADR-0011 §4, P3-3): read the durable
		// pause off the SAME store conductorctl writes it to, so `conductorctl pause`
		// in a separate process makes this daemon's next tick a clean no-op.
		Pauser: conductor.NewStorePauser(store),
		// Control reverse-channel ABORT watcher (ADR-0020 follow-up / F-2): read the
		// durable per-task abort signal off the SAME store conductorctl writes it to,
		// so `conductorctl abort` in a separate process cancels this daemon's in-flight
		// develop (killing the performer process group) and reverts the task to ready.
		Aborter: conductor.NewStoreAborter(store),
		// Governance human-hold APPROVE resolver (ADR-0003, N-10, Faz-1.5-b): read the
		// durable per-task approval off the SAME store conductorctl writes it to, so
		// `conductorctl approve` in a separate process makes this daemon's next tick
		// MERGE the held task's PRESERVED verified branch without re-developing.
		Approver: conductor.NewStoreApprover(store),
		// Sentinel 3-layer liveness progress-watchdog (ADR-0006, Faz-2 2C-1). Nil unless
		// -sentinel is set, so the default daemon behavior is unchanged (Layer-1+3-only
		// via -timeout). When wired, the Layer-3 backstop ALWAYS overrides the gray-zone
		// advisor — the LLM can never make a run wait past the ceiling.
		Sentinel: newSentinel(cfg, logger),
	})
	if err != nil {
		closer()
		return nil, fmt.Errorf("conductor: %w", err)
	}

	// NewWriter returns nil for an empty path; a nil *Writer is a no-op, so an
	// unconfigured heartbeat changes nothing about the daemon's behavior.
	hb := heartbeat.NewWriter(cfg.heartbeatPath, cfg.hostID, cfg.project, os.Getpid(), nil)

	// Last-tick snapshot + OPTIONAL health server (P4-2). The snapshot is always
	// allocated so tick can record unconditionally; the health server is built only
	// when -http-addr is set. Readiness probes the store via a cheap ListProjects on
	// the FROZEN interface (no interface change, no concrete type-assert). The store
	// backend name is derived from cfg.dsn (empty=memory) so /status never sees the
	// DSN.
	snap := &tickSnapshot{}
	var health *healthServer
	if cfg.httpAddr != "" {
		now := time.Now()
		health = &healthServer{
			project:      cfg.project,
			storeBackend: storeBackendName(cfg.dsn),
			governance:   cfg.governance,
			startedAt:    now,
			clock:        time.Now,
			snap:         snap,
			ready:        storeReadyChecker{store: store},
		}
	}

	return &Daemon{
		cond:       cond,
		project:    cfg.project,
		interval:   cfg.interval,
		once:       cfg.once,
		logger:     logger,
		store:      store,
		hostID:     cfg.hostID,
		hbInterval: defaultHeartbeatInterval,
		hb:         hb,
		health:     health,
		httpAddr:   cfg.httpAddr,
		snap:       snap,
		closer:     closer,
	}, nil
}

// storeBackendName maps the DSN to the human backend name surfaced by /status.
// Empty DSN means the in-memory store; any non-empty DSN means Postgres. It
// returns ONLY the backend name — never the DSN — so /status stays secret-free.
func storeBackendName(dsn string) string {
	if dsn == "" {
		return "memory"
	}
	return "postgres"
}

// newStore selects and constructs the StateStore backend from cfg.dsn and returns
// it alongside a closer that releases any backend resource (the Postgres pool).
// The closer is always non-nil: for the in-memory store it is a no-op, so callers
// can defer/call it unconditionally. The active backend is logged WITHOUT the DSN
// (which carries the password) — only the backend name is emitted.
func newStore(ctx context.Context, cfg config, logger *slog.Logger) (statestore.StateStore, func(), error) {
	if cfg.dsn == "" {
		logger.Info("statestore backend selected", slog.String("backend", "memory"))
		return statestore.NewMemoryStore(), func() {}, nil
	}

	pg, err := statestore.NewPostgresStore(ctx, cfg.dsn)
	if err != nil {
		// err may wrap the DSN-derived connection error from pgx, but pgx does not
		// echo the password in its message; we still avoid logging cfg.dsn ourselves.
		return nil, nil, fmt.Errorf("open postgres store: %w", err)
	}
	if err := pg.Migrate(ctx); err != nil {
		pg.Close()
		return nil, nil, fmt.Errorf("migrate postgres store: %w", err)
	}
	logger.Info("statestore backend selected", slog.String("backend", "postgres"))
	return pg, pg.Close, nil
}

// newEmitter selects and constructs the event bus (ADR-0011, N-9) the conductor
// publishes lifecycle events on, returning it as a conductor.Emitter alongside a
// closer that releases any backend resource. The closer is always non-nil: for
// the in-memory bus it tears down subscriptions (a safe no-op here, as the daemon
// holds no subscribers), so callers can call it unconditionally.
//
// The backend follows cfg.dsn, mirroring newStore: empty selects the in-memory
// bus (events observable in-process; fine for dev), non-empty selects the
// Postgres LISTEN/NOTIFY bus so external subscribers on other processes/hosts see
// events in realtime. The PG bus opens its OWN pgx pool (a dedicated LISTENer
// connection is required and the frozen statestore exposes no pool accessor); the
// events table it persists to is created by the statestore migrations the store
// already applied on startup. The DSN (which carries the password) is NEVER
// logged — only the backend name is emitted.
//
// An events.EventBus satisfies conductor.Emitter directly: both declare
// Publish(ctx, events.Event) error, so no adapter is needed.
func newEmitter(ctx context.Context, cfg config, logger *slog.Logger) (conductor.Emitter, func(), error) {
	if cfg.dsn == "" {
		bus := events.NewMemoryBus()
		logger.Info("event bus backend selected", slog.String("backend", "memory"))
		return bus, bus.Close, nil
	}

	bus, err := events.NewPostgresBus(ctx, cfg.dsn)
	if err != nil {
		// pgx does not echo the password in its error; we still never log cfg.dsn.
		return nil, nil, fmt.Errorf("open postgres event bus: %w", err)
	}
	logger.Info("event bus backend selected", slog.String("backend", "postgres"))
	return bus, bus.Close, nil
}

// newPolicy constructs the risk-layered governance merge policy (ADR-0003, N-10)
// when cfg.governance is true (the default), returning nil when it is false. A nil
// conductor.Policy means auto-merge-all (the pre-N-10 behavior). It is split from
// newDaemon so a test can assert the on/off → non-nil/nil mapping deterministically
// without reaching into the conductor's unexported seams. It logs the active mode
// (never any secret).
func newPolicy(cfg config, logger *slog.Logger) conductor.Policy {
	if !cfg.governance {
		logger.Info("governance policy disabled", slog.String("mode", "auto-merge-all"))
		return nil
	}
	logger.Info("governance policy active", slog.String("mode", "risk-layered (T3/T4 + untiered held for human)"))
	return governance.DefaultPolicy()
}

// defaultGates is the built-in deterministic verify recipe (FIX #1): go build +
// go test + go vet. All three ship with the Go toolchain, so they are ALWAYS
// available — vet is safe to include in the default, hardening the daemon's merge
// gate to match the platform's own `make gate` for the build/test/vet legs.
// golangci-lint is deliberately NOT here: it is an external binary the shipped
// daemon/container may not have, so lint is opt-in via a .conductor recipe.
func defaultGates() []verify.Gate {
	return []verify.Gate{
		{Name: "go build", Argv: []string{"go", "build", "./..."}},
		{Name: "go test", Argv: []string{"go", "test", "./..."}},
		{Name: "go vet", Argv: []string{"go", "vet", "./..."}},
	}
}

// resolveRecipe picks the PER-PROJECT recipe (2A-1): both the develop command and
// the verify gates. When cfg.recipeDir is set AND that repo has a scaffolder-emitted
// .conductor/config.yaml, BOTH the develop command and the gates come from that
// config (reusing the scaffolder's exact shape, closing the N-8 scaffolder→daemon
// gap) — this is how a PROJECT declares its OWN performer/recipe and how an operator
// opts INTO golangci-lint. When no recipe dir is configured, OR the configured dir
// has no .conductor/config.yaml, it falls back to the global -develop-cmd flag
// (cfg.developCmd) + defaultGates (build+test+vet) so a config-less project keeps
// working EXACTLY as before (backward compatible).
//
// The recipe's develop command is honored ONLY when it is non-empty AND not the
// inert onboarding placeholder (developPlaceholder, an un-reviewed draft): a
// placeholder/empty develop falls back to the global -develop-cmd flag so an
// un-confirmed recipe can never silently launch a no-op performer. The chosen
// develop SOURCE is logged (file vs flag).
//
// A configured recipe that opts into golangci-lint does NOT make the daemon check
// for the binary here: a missing golangci-lint is caught at gate-run time, where
// the gate FAILS deterministically with the exec error (never silently skipped —
// that would be a fake-green). Operators who configure golangci must install it.
func resolveRecipe(cfg config, logger *slog.Logger) (develop []string, gates []verify.Gate, err error) {
	if cfg.recipeDir == "" {
		logger.Info("recipe selected",
			slog.String("source", "flags/default (no -recipe-dir)"),
			slog.String("develop", "from -develop-cmd flag"),
			slog.String("gates", "go build, go test, go vet"))
		return cfg.developCmd, defaultGates(), nil
	}

	rec, lerr := scaffolder.LoadRecipe(cfg.recipeDir)
	if lerr != nil {
		if errors.Is(lerr, scaffolder.ErrNoRecipe) {
			logger.Info("recipe selected",
				slog.String("source", "flags/default (no .conductor/config.yaml in recipe-dir)"),
				slog.String("recipe_dir", cfg.recipeDir),
				slog.String("develop", "from -develop-cmd flag"),
				slog.String("gates", "go build, go test, go vet"))
			return cfg.developCmd, defaultGates(), nil
		}
		// A present-but-broken recipe must fail loud, not degrade to weaker gates.
		return nil, nil, lerr
	}

	gates = make([]verify.Gate, 0, len(rec.Gates))
	names := make([]string, 0, len(rec.Gates))
	for _, s := range rec.Gates {
		gates = append(gates, verify.Gate{Name: s.Name, Argv: s.Argv})
		names = append(names, s.Name)
	}

	// Per-project develop: honor the recipe's develop command only when it is a
	// real, human-confirmed command — not empty and not the inert placeholder. An
	// un-reviewed draft falls back to the global flag rather than running a no-op.
	develop = cfg.developCmd
	developSource := "from -develop-cmd flag (recipe develop empty/placeholder)"
	if len(rec.Develop) > 0 && !isDevelopPlaceholder(rec.Develop) {
		develop = rec.Develop
		developSource = "from .conductor/config.yaml (per-project)"
	}

	logger.Info("recipe selected",
		slog.String("source", ".conductor/config.yaml"),
		slog.String("recipe_dir", cfg.recipeDir),
		slog.String("develop", developSource),
		slog.String("gates", strings.Join(names, ", ")),
		slog.String("note", "a configured golangci-lint gate requires the binary installed; a missing binary fails the gate deterministically"))
	return develop, gates, nil
}

// isDevelopPlaceholder reports whether argv is the scaffolder's inert onboarding
// develop placeholder (`echo configure-develop-command`). An un-reviewed draft
// carries this no-op so the daemon never silently launches it as a performer; the
// daemon falls back to the global -develop-cmd flag instead. It mirrors
// scaffolder.developPlaceholder (which is unexported) by value, not by importing it.
func isDevelopPlaceholder(argv []string) bool {
	return len(argv) == 2 && argv[0] == "echo" && argv[1] == "configure-develop-command"
}

// newVerifier constructs the independent verify gate (B-2) with the holdout store
// selected by config (ADR-0018, A.1, Faz-1.5-a). The holdout source is now a
// SCHEME-ROUTING holdout.Router that dispatches each scenario's HoldoutRef by its
// scheme to whichever backing the operator configured:
//
//	store://  -> filesystem FSStore       when -holdout-store <dir> is set
//	pg://     -> Postgres PGStore         when -dsn is set (shares that DSN; the
//	                                       holdouts table is created by the same
//	                                       statestore migrations)
//	private:  -> PrivateRepoStore         when -holdout-private-cache <dir> is set
//	                                       (gh-token from CONDUCTOR_GH_TOKEN/GH_TOKEN)
//
// When NONE of these are configured (the DEFAULT) it keeps the inert noopHoldout
// + ["true"] so the daemon's behavior is unchanged (backward compatible). A ref
// whose scheme has no configured backing is a CLEAR runtime error at fetch time,
// not a silent skip (Rule#9). It returns a closer that releases the holdout PG
// pool (if the pg:// backing opened one); the closer is always non-nil.
//
// It logs WHICH schemes are active — including the fs root and private cache
// paths, which are operator config, not secrets — but NEVER the DSN password or
// the gh-token, and never holdout CONTENTS.
func newVerifier(ctx context.Context, cfg config, logger *slog.Logger) (*verify.Verifier, func(), error) {
	noClose := func() {}

	var opts []holdout.RouterOption

	// store:// filesystem backing.
	if cfg.holdoutStore != "" {
		fsStore, err := holdout.New(cfg.holdoutStore)
		if err != nil {
			return nil, noClose, err
		}
		opts = append(opts, holdout.WithFS(fsStore))
	}

	// pg:// Postgres backing. Auto-available when -dsn is set: it opens its OWN pgx
	// pool against the same DSN (the frozen statestore exposes no pool accessor, so
	// this mirrors the event bus), and the holdouts table is created by the
	// statestore migrations the store already applied on startup.
	closer := noClose
	if cfg.dsn != "" {
		pool, err := pgxpool.New(ctx, cfg.dsn)
		if err != nil {
			// pgx does not echo the password in its error; we still never log cfg.dsn.
			return nil, noClose, fmt.Errorf("open holdout pg pool: %w", err)
		}
		pgStore, err := holdout.NewPG(pool)
		if err != nil {
			pool.Close()
			return nil, noClose, err
		}
		opts = append(opts, holdout.WithPG(pgStore))
		closer = pool.Close
	}

	// private: git-repo backing. Configured when a cache dir is supplied; the
	// gh-token (env-only) is never logged.
	if cfg.holdoutPrivateCache != "" {
		privStore, err := holdout.NewPrivate(holdout.PrivateConfig{
			CacheDir: cfg.holdoutPrivateCache,
			GHToken:  cfg.holdoutGHToken,
		})
		if err != nil {
			closer()
			return nil, noClose, err
		}
		opts = append(opts, holdout.WithPrivate(privStore))
	}

	if len(opts) == 0 {
		logger.Info("holdout mode selected",
			slog.String("mode", "noop"),
			slog.String("detail", "no holdout backing (-holdout-store / -dsn / -holdout-private-cache unset); merge gate rides on public recipe gates only (ADR-0018 holdout inert)"))
		return verify.New(noopHoldout{}, verify.Config{HoldoutCmd: []string{"true"}}), noClose, nil
	}

	router, err := holdout.NewRouter(opts...)
	if err != nil {
		closer()
		return nil, noClose, err
	}
	logger.Info("holdout mode selected",
		slog.String("mode", "router"),
		slog.String("schemes", strings.Join(router.ActiveSchemes(), ", ")),
		slog.String("fs_root", cfg.holdoutStore),
		slog.String("private_cache", cfg.holdoutPrivateCache),
		slog.Bool("private_has_token", cfg.holdoutPrivateCache != "" && cfg.holdoutGHToken != ""),
		slog.String("cmd", strings.Join(cfg.holdoutCmd, " ")))
	return verify.New(router, verify.Config{HoldoutCmd: cfg.holdoutCmd}), closer, nil
}

// noopHoldout is an inert HoldoutStore: the daemon's merge decision rides on the
// deterministic recipe gates (go build / go test) over the performer's output,
// not on an injected holdout (which is exercised by verify's own tests). It
// mirrors the e2e test's noopHoldout so the daemon's wiring matches the proven
// path.
type noopHoldout struct{}

func (noopHoldout) Fetch(_ context.Context, _ string) (verify.Holdout, error) {
	return verify.Holdout{Name: "daemon-noop", Files: nil}, nil
}

// Run drives the conductor: a single tick in once mode, otherwise a tick every
// interval until ctx is cancelled (SIGINT/SIGTERM). It returns the tick error in
// once mode (so CI sees a non-zero exit on failure); in loop mode a per-tick
// error is logged and the loop continues — one bad task must not kill the daemon.
// A cancelled context is a clean shutdown, not an error.
// heartbeatNow stamps the host-registry heartbeat once, best-effort (C-1). It is
// the single write the goroutine repeats and the one -once mode does directly. The
// write uses a stand-alone context (not the tick/loop ctx) so an in-flight
// shutdown unwind does not cancel a heartbeat mid-flight; a write error is logged
// but never propagated — liveness telemetry must not break or stop the daemon. A
// nil store (defensive) is a no-op.
func (d *Daemon) heartbeatNow() {
	if d.store == nil {
		return
	}
	clock := d.hbClock
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	if hbErr := d.store.HostHeartbeat(context.Background(), d.hostID, clock()); hbErr != nil {
		d.logger.Warn("host heartbeat failed", slog.String("host", d.hostID), slog.String("err", hbErr.Error()))
	}
}

// runHostHeartbeat advances this host's registry LastHeartbeat on a FIXED cadence
// (hbInterval, default 30s) for the daemon's lifetime, until ctx is cancelled (C-1,
// ADR-0024 agent-per-host). It is the fix for host-heartbeat starvation: because it
// runs in its OWN goroutine, the heartbeat keeps firing every hbInterval even while
// a tick blocks for a long develop (up to -timeout 30m) — so the reconcile reaper
// (HostHeartbeatOwnerLive, host-stale 2m default) always sees this LIVE host as
// fresh and never false-reaps its active lease (which would let a second host
// acquire the same repo, violating repo-per-1, ADR-0008). A genuinely dead daemon
// stops the goroutine (ctx cancelled / process gone), its heartbeat goes stale, and
// the reaper correctly frees the lease.
//
// It fires an immediate heartbeat on entry (so liveness is fresh the instant the
// loop starts, not one interval later) and then on every tick of the ticker.
func (d *Daemon) runHostHeartbeat(ctx context.Context) {
	interval := d.hbInterval
	if interval <= 0 {
		interval = defaultHeartbeatInterval
	}
	d.heartbeatNow()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.heartbeatNow()
		}
	}
}

func (d *Daemon) Run(ctx context.Context) error {
	if d.once {
		// -once: no loop, so no heartbeat goroutine — a single heartbeat keeps this
		// host's registry liveness fresh for the one pass (C-1).
		d.heartbeatNow()
		_, err := d.tick(ctx)
		return err
	}

	d.logger.Info("conductor daemon started",
		slog.String("project", d.project),
		slog.Duration("interval", d.interval))

	// Start the OPTIONAL health HTTP server alongside the loop (P4-2). It is nil
	// when no -http-addr is configured, in which case this is a no-op. The returned
	// shutdown closure is deferred so the server is drained on EVERY exit path
	// (ctx cancel / SIGINT-SIGTERM), with no goroutine or socket leak.
	stopHealth := d.startHealthServer(ctx)
	defer stopHealth()

	// Background host-registry heartbeat (C-1): a DEDICATED goroutine advances this
	// host's LastHeartbeat every hbInterval for the daemon's lifetime, INDEPENDENT
	// of how long any tick blocks for a develop. It is bound to a child context
	// cancelled on Run's return, and we wait for it to exit before returning so the
	// goroutine never outlives the daemon (no leak, clean shutdown).
	hbCtx, stopHB := context.WithCancel(ctx)
	hbDone := make(chan struct{})
	go func() {
		defer close(hbDone)
		d.runHostHeartbeat(hbCtx)
	}()
	defer func() {
		stopHB()
		<-hbDone
	}()

	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	// Tick once immediately so the daemon does not idle for a whole interval before
	// doing any work.
	if _, err := d.tick(ctx); err != nil {
		d.logger.Error("tick failed", slog.String("err", err.Error()))
	}

	for {
		select {
		case <-ctx.Done():
			d.logger.Info("conductor daemon stopping (signal received); in-flight tick finished")
			// Final heartbeat on graceful shutdown: a "shutdown" outcome lets the
			// independent detector distinguish a clean stop from a crash (the file
			// then goes stale, not torn). Best-effort; a write error is logged only.
			if hbErr := d.hb.Write(d.ticks, "shutdown"); hbErr != nil {
				d.logger.Warn("final heartbeat write failed", slog.String("err", hbErr.Error()))
			}
			return nil
		case <-ticker.C:
			if _, err := d.tick(ctx); err != nil {
				// A tick error in loop mode is logged, not fatal: the next tick
				// re-derives from the store (idempotent, ADR-0001).
				d.logger.Error("tick failed", slog.String("err", err.Error()))
			}
		}
	}
}

// startHealthServer launches the OPTIONAL health HTTP server (P4-2) in a
// goroutine and returns a shutdown closure the caller defers. When no -http-addr
// is configured (d.health is nil), both are no-ops and the daemon behaves exactly
// as before.
//
// Lifecycle: the listener is bound SYNCHRONOUSLY (so a bad/occupied address is
// logged immediately, not swallowed in a goroutine) and ListenAndServe runs in
// the goroutine. A watcher goroutine drains the server with a bounded grace
// period when ctx is cancelled — the SAME ctx the tick loop unwinds on — so the
// server stops with the loop on SIGINT/SIGTERM. The returned closure also drains
// on ANY Run exit path, making the teardown idempotent and leak-free.
func (d *Daemon) startHealthServer(ctx context.Context) func() {
	if d.health == nil {
		return func() {}
	}

	srv := &http.Server{
		Addr:              d.httpAddr,
		Handler:           d.health.routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// Bind synchronously so an unusable address surfaces now (and disables the
	// server) instead of failing silently inside the serve goroutine.
	ln, err := net.Listen("tcp", d.httpAddr)
	if err != nil {
		d.logger.Error("health server: listen failed; health endpoints disabled",
			slog.String("addr", d.httpAddr), slog.String("err", err.Error()))
		return func() {}
	}
	d.boundMu.Lock()
	d.boundAddr = ln.Addr().String()
	d.boundMu.Unlock()
	d.logger.Info("health server listening",
		slog.String("addr", ln.Addr().String()),
		slog.String("endpoints", "/healthz /readyz /status"))

	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		if serveErr := srv.Serve(ln); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			d.logger.Error("health server: serve error", slog.String("err", serveErr.Error()))
		}
	}()

	// Drain on ctx cancel (loop shutdown) with a bounded grace period.
	go func() {
		<-ctx.Done()
		d.shutdownHealth(srv)
	}()

	// shutdown closure: idempotent drain for any Run exit path. http.Server.Shutdown
	// is safe to call more than once (a second call returns immediately), so racing
	// the watcher goroutine is harmless.
	var once sync.Once
	return func() {
		once.Do(func() { d.shutdownHealth(srv) })
		<-serveDone
	}
}

// shutdownHealth gracefully drains the health server with a bounded grace period
// so in-flight probes finish but a hung connection cannot stall shutdown. Errors
// are logged, never fatal — health teardown must not block the daemon's exit.
func (d *Daemon) shutdownHealth(srv *http.Server) {
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		d.logger.Warn("health server: shutdown error", slog.String("err", err.Error()))
	}
}

// boundHealthAddr returns the actual address the health listener bound, or "" if
// the server is disabled or has not bound yet. It exists so an end-to-end test can
// discover the OS-assigned ephemeral port; production code reads the logged addr.
func (d *Daemon) boundHealthAddr() string {
	d.boundMu.Lock()
	defer d.boundMu.Unlock()
	return d.boundAddr
}

// tick runs one Conductor.Tick and emits a structured log line describing what
// happened: outcome, the task picked (if any), the self-reported verdict and the
// INDEPENDENT review verdict (the actual merge gate, Rule#9), and the merge SHA on
// a merge. The error, if any, is both logged and returned so once-mode can
// propagate it as a non-zero exit.
func (d *Daemon) tick(ctx context.Context) (conductor.TickResult, error) {
	start := time.Now()
	d.logger.Info("tick start", slog.String("project", d.project))

	res, err := d.cond.Tick(ctx, d.project)

	// Heartbeat (ADR-0016): record this tick whether it succeeded or failed, so a
	// stuck/erroring daemon still emits liveness AND a progress signal (the tick
	// count advances even on a failed tick). The outcome captures error context
	// for the independent detector to log. A heartbeat-write error is logged but
	// never fails the tick — liveness telemetry must not break the daemon.
	d.ticks++
	outcome := string(res.Outcome)
	if err != nil {
		outcome = "error:" + string(res.Outcome)
	}
	if hbErr := d.hb.Write(d.ticks, outcome); hbErr != nil {
		d.logger.Warn("heartbeat write failed", slog.String("err", hbErr.Error()))
	}

	// NOTE (C-1): the host-registry heartbeat is NO LONGER advanced here. A long
	// develop (up to -timeout 30m) would leave LastHeartbeat stale for the whole
	// tick, and the reconcile reaper (HostHeartbeatOwnerLive, host-stale 2m) would
	// then false-reap this LIVE host's lease → repo-per-1 (ADR-0008) violation. The
	// heartbeat is now driven by a DEDICATED background goroutine (runHostHeartbeat),
	// started by Run in loop mode and firing every hbInterval INDEPENDENT of tick
	// progress, so a busy host stays fresh. -once mode does a single heartbeat in Run.

	// Record the last-tick snapshot /status reads (P4-2). The snapshot is always
	// non-nil; it is concurrency-safe so the HTTP handler never races this write.
	d.snap.record(d.ticks, outcome, time.Now())

	attrs := []any{
		slog.String("project", d.project),
		slog.String("outcome", string(res.Outcome)),
		slog.String("task", res.TaskID),
		slog.Duration("elapsed", time.Since(start)),
	}
	if res.Verdict.Result != "" {
		attrs = append(attrs, slog.String("verdict", res.Verdict.Result))
	}
	if res.Review.Result != "" {
		attrs = append(attrs, slog.String("review", res.Review.Result))
	}
	if res.MergeSHA != "" {
		attrs = append(attrs, slog.String("merge_sha", res.MergeSHA))
	}
	if res.DenyReason != "" {
		attrs = append(attrs, slog.String("deny_reason", string(res.DenyReason)))
	}

	if err != nil {
		attrs = append(attrs, slog.String("err", err.Error()))
		d.logger.Error("tick error", attrs...)
		return res, err
	}
	d.logger.Info("tick done", attrs...)
	return res, nil
}

// splitArgs splits a command string into an argv with QUOTE-AWARE, shell-like
// word splitting — but the result is executed WITHOUT a shell, so NO `$`
// expansion, NO pipes, and NO globbing ever happen: it is pure tokenization.
//
// Rules:
//   - Bare whitespace (space/tab/newline) separates tokens.
//   - A `'...'` chunk is a literal token chunk: every byte until the closing `'`
//     is taken verbatim (no escapes inside single quotes).
//   - A `"..."` chunk groups text and honors only the escapes `\"` and `\\`
//     (a backslash before any other byte is kept literally, like the shell).
//   - Adjacent quoted/unquoted chunks with no whitespace between them collapse
//     into ONE token, so `-p"a b"` -> `-pa b` and `'a'b` -> `ab`.
//   - The empty (or all-whitespace) string yields an empty slice.
//   - An unterminated single or double quote is a clear error.
//
// The no-quote case is identical to strings.Fields: `claude -p` -> ["claude","-p"].
func splitArgs(s string) ([]string, error) {
	var (
		args   []string
		cur    strings.Builder
		hasTok bool // a token is in progress (even if empty, e.g. "" -> one empty arg)
	)
	flush := func() {
		if hasTok {
			args = append(args, cur.String())
			cur.Reset()
			hasTok = false
		}
	}

	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch c {
		case ' ', '\t', '\n', '\r', '\f', '\v':
			flush()
		case '\'':
			hasTok = true
			j := i + 1
			for j < len(runes) && runes[j] != '\'' {
				cur.WriteRune(runes[j])
				j++
			}
			if j >= len(runes) {
				return nil, fmt.Errorf("unterminated single quote in %q", s)
			}
			i = j // skip the closing quote
		case '"':
			hasTok = true
			j := i + 1
			closed := false
			for j < len(runes) {
				if runes[j] == '\\' && j+1 < len(runes) {
					next := runes[j+1]
					if next == '"' || next == '\\' {
						cur.WriteRune(next)
						j += 2
						continue
					}
					// A backslash before any other byte is kept literally.
					cur.WriteRune('\\')
					j++
					continue
				}
				if runes[j] == '"' {
					closed = true
					break
				}
				cur.WriteRune(runes[j])
				j++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated double quote in %q", s)
			}
			i = j // skip the closing quote
		default:
			hasTok = true
			cur.WriteRune(c)
		}
	}
	flush()
	return args, nil
}

// splitCapabilities parses a comma-separated capability list into a normalized,
// deterministic slice (ADR-0024): each entry is trimmed of surrounding whitespace,
// empty entries are dropped, and the result is sorted + de-duplicated so a host's
// registered capability set is stable regardless of input order or spacing. The
// empty (or all-blank) string yields a nil slice — a host with no capabilities,
// which still registers and matches only no-requires lanes (backward compatible).
func splitCapabilities(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, p := range parts {
		c := strings.TrimSpace(p)
		if c == "" {
			continue
		}
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

// envOr returns the value of env var key, or def when it is unset/empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envDurationOr returns the duration parsed from env var key, or def when it is
// unset or unparseable.
func envDurationOr(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

// envBoolOr returns the bool parsed from env var key, or def when unset/unparseable.
func envBoolOr(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

// envIntOr returns the int parsed from env var key, or def when unset/unparseable.
func envIntOr(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// envFloatOr returns the float parsed from env var key, or def when unset/unparseable.
func envFloatOr(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}
