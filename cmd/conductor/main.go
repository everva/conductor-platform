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
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/everva/conductor-platform/internal/conductor"
	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/events"
	"github.com/everva/conductor-platform/internal/governance"
	"github.com/everva/conductor-platform/internal/governor"
	"github.com/everva/conductor-platform/internal/heartbeat"
	"github.com/everva/conductor-platform/internal/provisioner"
	"github.com/everva/conductor-platform/internal/registry"
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
	// is split on spaces into an argv (no shell). Override with -develop-cmd.
	defaultDevelopCmd = "claude -p"
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
		"performer command run in the worktree (space-split argv, no shell; subscription claude -p style, no key)")
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
	governance := fs.Bool("governance", envBoolOr("CONDUCTOR_GOVERNANCE", true),
		"wire the risk-layered merge policy (ADR-0003): high-tier/untiered tasks are HELD for a human after a green gate; false = auto-merge all (default true)")

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

	cmd := splitArgv(*developCmd)
	if len(cmd) == 0 {
		return config{}, errors.New("-develop-cmd must not be empty")
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
		governance:        *governance,
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

	verf := verify.New(noopHoldout{}, verify.Config{HoldoutCmd: []string{"true"}})

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

	cond, err := conductor.New(conductor.Deps{
		Store:       store,
		Picker:      registry.NewRegistry(store),
		Provisioner: prov,
		Engine:      eng,
		Verifier:    verf,
		Merger:      merger,
		Recipe: conductor.Recipe{Gates: []verify.Gate{
			{Name: "go build", Argv: []string{"go", "build", "./..."}},
			{Name: "go test", Argv: []string{"go", "test", "./..."}},
		}},
		HostID:   cfg.hostID,
		Governor: gov,
		Emitter:  emitter,
		Policy:   policy,
		// Control reverse-channel pause-gate (ADR-0011 §4, P3-3): read the durable
		// pause off the SAME store conductorctl writes it to, so `conductorctl pause`
		// in a separate process makes this daemon's next tick a clean no-op.
		Pauser: conductor.NewStorePauser(store),
	})
	if err != nil {
		closer()
		return nil, fmt.Errorf("conductor: %w", err)
	}

	// NewWriter returns nil for an empty path; a nil *Writer is a no-op, so an
	// unconfigured heartbeat changes nothing about the daemon's behavior.
	hb := heartbeat.NewWriter(cfg.heartbeatPath, cfg.hostID, cfg.project, os.Getpid(), nil)

	return &Daemon{
		cond:     cond,
		project:  cfg.project,
		interval: cfg.interval,
		once:     cfg.once,
		logger:   logger,
		hb:       hb,
		closer:   closer,
	}, nil
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

// splitArgv splits a command string into an argv on whitespace, dropping empty
// fields. It is intentionally simple (no shell quoting): the develop command is
// an operator-supplied program + flags, not a shell pipeline.
func splitArgv(s string) []string {
	return strings.Fields(s)
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
