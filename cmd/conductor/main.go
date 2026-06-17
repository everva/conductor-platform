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
	"github.com/everva/conductor-platform/internal/governor"
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

	d, err := newDaemon(cfg, logger)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "conductor: %v\n", err)
		return 1
	}

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

// parseConfig parses argv into a config, applying env fallback per flag and
// validating the required fields. Flag values win over env; env wins over the
// built-in default.
func parseConfig(argv []string, stderr io.Writer) (config, error) {
	fs := flag.NewFlagSet("conductor", flag.ContinueOnError)
	fs.SetOutput(stderr)

	project := fs.String("project", envOr("CONDUCTOR_PROJECT", ""), "project id to tick (required)")
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

	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "conductor — Faz-1a tick daemon")
		_, _ = fmt.Fprintln(stderr, "\nusage: conductor -project <id> -root <dir> [-once] [-interval 30s] [-develop-cmd \"claude -p\"]")
		fs.PrintDefaults()
	}

	if err := fs.Parse(argv); err != nil {
		return config{}, err
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
		project:     *project,
		rootDir:     *rootDir,
		baseBranch:  *baseBranch,
		interval:    *interval,
		timeout:     *timeout,
		developCmd:  cmd,
		once:        *once,
		hostID:      host,
		globalCap:   *globalCap,
		loadCeiling: *loadCeiling,
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
}

// newDaemon wires the SAME components the conductor e2e test wires (in-memory
// store, registry Picker, real provisioner, product CommandEngine, verify gate,
// GitMerger) into a Conductor and seeds the project so a tick has something to
// resolve. The develop command is the operator-supplied performer; it carries no
// key. Postgres-backed state and ledger seeding via conductorctl arrive in later
// tasks — for now the project record is created in-memory so the daemon is a
// self-contained, runnable spine.
func newDaemon(cfg config, logger *slog.Logger) (*Daemon, error) {
	store := statestore.NewMemoryStore()

	// Seed the project the daemon ticks. PickReady reads tasks from the same store;
	// a fresh in-memory store has no ready tasks, so an unseeded daemon cleanly
	// no-ops (which is exactly what the -once hermetic test asserts). Faz-1b swaps
	// this for the shared Postgres store conductorctl writes to.
	if err := store.CreateProject(context.Background(), statestore.Project{
		ID:         cfg.project,
		BaseBranch: cfg.baseBranch,
	}); err != nil && !errors.Is(err, statestore.ErrAlreadyExists) {
		return nil, fmt.Errorf("seed project %q: %w", cfg.project, err)
	}

	prov, err := provisioner.New(provisioner.Config{RootDir: cfg.rootDir})
	if err != nil {
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
		return nil, fmt.Errorf("governor: %w", err)
	}

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
	})
	if err != nil {
		return nil, fmt.Errorf("conductor: %w", err)
	}

	return &Daemon{
		cond:     cond,
		project:  cfg.project,
		interval: cfg.interval,
		once:     cfg.once,
		logger:   logger,
	}, nil
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
