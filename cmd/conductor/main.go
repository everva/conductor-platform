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
	"github.com/everva/conductor-platform/internal/registry"
	"github.com/everva/conductor-platform/internal/scaffolder"
	"github.com/everva/conductor-platform/internal/statestore"
	"github.com/everva/conductor-platform/internal/verify"
)

const (
	// defaultBaseBranch is the integration branch tasks are merged into when no
	// -base / CONDUCTOR_BASE_BRANCH is supplied (ADR-0004).
	defaultBaseBranch = "develop"
	// defaultInterval is the gap between ticks in loop mode.
	defaultInterval = 30 * time.Second
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
	// defaultHeartbeatStale is the age past which the independent stall-detector
	// (-check) considers the heartbeat STALE. Generous relative to the default
	// 30s tick interval so a single slow tick does not false-alert; an external
	// alert job overrides via -heartbeat-stale / CONDUCTOR_HEARTBEAT_STALE.
	defaultHeartbeatStale = 5 * time.Minute
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
	// globalCap is the governor's max concurrent tasks across all projects
	// (ADR-0008). The lease table is the live count; this caps it.
	globalCap int
	// loadCeiling is the governor's normalized-load ceiling (loadavg / NumCPU)
	// above which new work is denied admission (ADR-0008 "host yük tavanı").
	loadCeiling float64
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
	// recipeDir, when set, is the repo directory whose `.conductor/config.yaml`
	// (emitted by the scaffolder, ADR-0009) the daemon reads the verify gate recipe
	// from. Empty (the DEFAULT) uses the built-in default gates (go build + go test +
	// go vet) so a config-less project keeps working unchanged. A configured recipe
	// can opt INTO golangci-lint; if it does, golangci-lint MUST be installed or the
	// gate fails deterministically (never silently skipped).
	recipeDir string
	// governance, when true (the DEFAULT), wires the risk-layered merge policy
	// (governance.DefaultPolicy, ADR-0003/N-10) into the conductor so high-tier
	// (T3/T4) and untiered tasks are HELD for a human after a green gate instead of
	// auto-merging; T1/T2 still auto-merge. When false, no policy is injected (nil)
	// so every green task auto-merges (the pre-N-10 behavior). Default true so
	// production correctly human-gates high-risk merges.
	governance bool
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
	interval := fs.Duration("interval", envDurationOr("CONDUCTOR_INTERVAL", defaultInterval), "gap between ticks in loop mode")
	timeout := fs.Duration("timeout", envDurationOr("CONDUCTOR_TIMEOUT", defaultTimeout), "per-develop subprocess timeout")
	once := fs.Bool("once", envBoolOr("CONDUCTOR_ONCE", false), "run a single tick and exit (exit 0 on success)")
	maxConc := fs.Int("max-concurrent", envIntOr("CONDUCTOR_MAX_CONCURRENT", 1), "max concurrent tasks (Faz-1a: must be 1)")
	globalCap := fs.Int("global-cap", envIntOr("CONDUCTOR_GLOBAL_CAP", governor.DefaultGlobalCap),
		"governor: max concurrent tasks across all projects (lease-derived; ADR-0008)")
	loadCeiling := fs.Float64("load-ceiling", envFloatOr("CONDUCTOR_LOAD_CEILING", governor.DefaultLoadCeiling),
		"governor: normalized 1-min load ceiling (loadavg/NumCPU) above which new work is denied")
	heartbeat := fs.String("heartbeat", envOr("CONDUCTOR_HEARTBEAT", ""),
		"path the daemon writes its liveness heartbeat to each tick (ADR-0016); empty = disabled")
	heartbeatStale := fs.Duration("heartbeat-stale", envDurationOr("CONDUCTOR_HEARTBEAT_STALE", defaultHeartbeatStale),
		"independent -check: heartbeat age past which the daemon is reported STALE")
	heartbeatProgress := fs.Duration("heartbeat-progress", envDurationOr("CONDUCTOR_HEARTBEAT_PROGRESS", 0),
		"independent -check: if >0, a fresh heartbeat whose tick never advances for this long is STALE (stuck)")
	check := fs.Bool("check", envBoolOr("CONDUCTOR_CHECK", false),
		"run the INDEPENDENT stall-detector over -heartbeat and exit (0=FRESH, non-zero=STALE/MISSING); does not start the daemon")
	httpAddr := fs.String("http-addr", envOr("CONDUCTOR_HTTP_ADDR", ""),
		"listen address for the OPTIONAL health HTTP server (/healthz /readyz /status), e.g. :8080; empty = disabled")
	governance := fs.Bool("governance", envBoolOr("CONDUCTOR_GOVERNANCE", true),
		"wire the risk-layered merge policy (ADR-0003): high-tier/untiered tasks are HELD for a human after a green gate; false = auto-merge all (default true)")
	holdoutStore := fs.String("holdout-store", envOr("CONDUCTOR_HOLDOUT_STORE", ""),
		"repo-EXTERNAL root dir the hidden holdout (ADR-0018) is resolved under; empty = inert noop holdout (backward compatible). Path is logged, contents are not")
	holdoutCmd := fs.String("holdout-cmd", envOr("CONDUCTOR_HOLDOUT_CMD", defaultHoldoutCmd),
		"argv that runs the injected holdout suite in the verify-worktree (quote-aware, no shell expansion); used only when -holdout-store is set")
	recipeDir := fs.String("recipe-dir", envOr("CONDUCTOR_RECIPE_DIR", ""),
		"repo dir whose .conductor/config.yaml (ADR-0009) supplies the verify gate recipe; empty = built-in default gates (go build + go test + go vet). A configured recipe may opt into golangci-lint, which must then be installed or the gate fails")

	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "conductor — Faz-1a tick daemon")
		_, _ = fmt.Fprintln(stderr, "\nusage: conductor -project <id> -root <dir> [-once] [-interval 30s] [-develop-cmd \"claude -p\"]")
		_, _ = fmt.Fprintln(stderr, "       conductor -check -heartbeat <path>   # independent stall-detector (ADR-0016)")
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
		globalCap:         *globalCap,
		loadCeiling:       *loadCeiling,
		heartbeatPath:     *heartbeat,
		heartbeatStale:    *heartbeatStale,
		heartbeatProgress: *heartbeatProgress,
		httpAddr:          *httpAddr,
		governance:        *governance,
		holdoutStore:      *holdoutStore,
		holdoutCmd:        hcmd,
		recipeDir:         *recipeDir,
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

	prov, err := provisioner.New(provisioner.Config{RootDir: cfg.rootDir})
	if err != nil {
		closer()
		return nil, fmt.Errorf("provisioner: %w", err)
	}

	eng := engine.NewCommandEngine(engine.RecipeConfig{
		DevelopCmd: cfg.developCmd,
		Timeout:    cfg.timeout,
	})

	verf, err := newVerifier(cfg, logger)
	if err != nil {
		closer()
		return nil, fmt.Errorf("verifier: %w", err)
	}

	merger := conductor.NewGitMerger(func(projectID string) string {
		return cfg.rootDir + "/clones/" + projectID
	})

	// Resource-governor (ADR-0008, N-5): admission control consulted before each
	// lease/develop. The global count is derived from the shared lease table
	// (ListLeases), never a parallel counter; host load is read through the
	// default system probe. Default config is permissive enough not to false-deny
	// on a normal dev machine.
	gov, err := governor.New(store, governor.SystemLoadProbe{}, governor.Config{
		GlobalCap:   cfg.globalCap,
		LoadCeiling: cfg.loadCeiling,
	})
	if err != nil {
		closer()
		return nil, fmt.Errorf("governor: %w", err)
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

	// Verify gate recipe (FIX #1): the deterministic merge gate. It defaults to the
	// always-available Go toolchain trio (build + test + vet) and can be UPGRADED by
	// a scaffolder-emitted .conductor/config.yaml (e.g. to add golangci-lint).
	gates, err := resolveGates(cfg, logger)
	if err != nil {
		closer()
		return nil, fmt.Errorf("resolve gates: %w", err)
	}

	cond, err := conductor.New(conductor.Deps{
		Store:       store,
		Picker:      registry.NewRegistry(store),
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
		cond:     cond,
		project:  cfg.project,
		interval: cfg.interval,
		once:     cfg.once,
		logger:   logger,
		hb:       hb,
		health:   health,
		httpAddr: cfg.httpAddr,
		snap:     snap,
		closer:   closer,
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

// resolveGates picks the verify gate recipe (FIX #1). When cfg.recipeDir is set
// and that repo has a scaffolder-emitted .conductor/config.yaml, the gates come
// from that config (reusing the scaffolder's exact shape, closing the N-8
// scaffolder→daemon gap) — this is how an operator opts INTO golangci-lint. When
// no recipe dir is configured, OR the configured dir has no .conductor/config.yaml,
// it falls back to defaultGates (build+test+vet) so a config-less project keeps
// working unchanged (backward compatible).
//
// A configured recipe that opts into golangci-lint does NOT make the daemon check
// for the binary here: a missing golangci-lint is caught at gate-run time, where
// the gate FAILS deterministically with the exec error (never silently skipped —
// that would be a fake-green). Operators who configure golangci must install it.
func resolveGates(cfg config, logger *slog.Logger) ([]verify.Gate, error) {
	if cfg.recipeDir == "" {
		logger.Info("verify recipe selected",
			slog.String("source", "default"),
			slog.String("gates", "go build, go test, go vet"))
		return defaultGates(), nil
	}

	specs, found, err := scaffolder.LoadRecipeGates(cfg.recipeDir)
	if err != nil {
		// A present-but-broken recipe must fail loud, not degrade to weaker gates.
		return nil, err
	}
	if !found {
		logger.Info("verify recipe selected",
			slog.String("source", "default (no .conductor/config.yaml in recipe-dir)"),
			slog.String("recipe_dir", cfg.recipeDir),
			slog.String("gates", "go build, go test, go vet"))
		return defaultGates(), nil
	}

	gates := make([]verify.Gate, 0, len(specs))
	names := make([]string, 0, len(specs))
	for _, s := range specs {
		gates = append(gates, verify.Gate{Name: s.Name, Argv: s.Argv})
		names = append(names, s.Name)
	}
	logger.Info("verify recipe selected",
		slog.String("source", ".conductor/config.yaml"),
		slog.String("recipe_dir", cfg.recipeDir),
		slog.String("gates", strings.Join(names, ", ")),
		slog.String("note", "a configured golangci-lint gate requires the binary installed; a missing binary fails the gate deterministically"))
	return gates, nil
}

// newVerifier constructs the independent verify gate (B-2) with the holdout store
// selected by cfg.holdoutStore (ADR-0018, A.1). When a -holdout-store root is
// configured, it wires the filesystem-backed holdout.FSStore so each scenario's
// repo-external HoldoutRef is fetched and injected into the verify-worktree, and
// the holdout suite runs cfg.holdoutCmd. When no root is configured (the DEFAULT),
// it keeps the inert noopHoldout + ["true"] so the daemon's behavior is unchanged
// (backward compatible). It logs WHICH holdout mode is active — including the store
// ROOT path, which is operator config, not a secret — but never holdout CONTENTS.
func newVerifier(cfg config, logger *slog.Logger) (*verify.Verifier, error) {
	if cfg.holdoutStore == "" {
		logger.Info("holdout mode selected",
			slog.String("mode", "noop"),
			slog.String("detail", "no -holdout-store; merge gate rides on public recipe gates only (ADR-0018 holdout inert)"))
		return verify.New(noopHoldout{}, verify.Config{HoldoutCmd: []string{"true"}}), nil
	}

	store, err := holdout.New(cfg.holdoutStore)
	if err != nil {
		return nil, err
	}
	logger.Info("holdout mode selected",
		slog.String("mode", "fs-store"),
		slog.String("root", store.Root()),
		slog.String("cmd", strings.Join(cfg.holdoutCmd, " ")))
	return verify.New(store, verify.Config{HoldoutCmd: cfg.holdoutCmd}), nil
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
func (d *Daemon) Run(ctx context.Context) error {
	if d.once {
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
