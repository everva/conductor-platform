// Command conductorctl is the thin Faz-1a operator client (PHASE-1-PLAN §1): a
// minimal-but-real CLI that onboards a repo, intakes a scenario, shows the ledger,
// and pauses/resumes the conductor loop. It drives the SAME frozen
// statestore.StateStore the daemon uses (no second source of truth) and exposes
// the run-state control as an injected Controller seam so Faz-1b can swap to the
// Postgres command table (ADR-0011/0010 §O5) without touching the handlers.
//
// It uses only the standard library `flag` plus a small subcommand dispatcher (no
// heavy framework). Exit codes are meaningful — 0 on success, non-zero on any
// error — and errors go to stderr.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/everva/conductor-platform/internal/statestore"
)

// defaultBaseBranch is the integration branch onboarded projects default to when
// no --base flag is given (ADR-0004 base branch).
const defaultBaseBranch = "develop"

func main() {
	ctx := context.Background()

	// -dsn is a GLOBAL flag that selects the StateStore backend the operator drives:
	// empty (default) keeps the Faz-1a in-memory store (each process fresh, fine for
	// dev/tests), while a non-empty DSN points conductorctl at the SAME shared
	// Postgres the daemon uses (ADR-0010/0013) so onboard/intake in one invocation
	// are visible to status/pause in a later, separate process — and to the running
	// daemon. It is extracted here, AROUND the subcommand dispatch, so the
	// per-subcommand flag parsing (--project/--file/--base) in run is untouched.
	dsn, rest, err := extractDSN(os.Args[1:])
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "conductorctl: %v\n", err)
		os.Exit(2)
	}

	// Select the StateStore backend from the resolved DSN. The closer releases the
	// Postgres pool (a no-op for memory) and is always invoked before exit. The DSN
	// (which carries the password) is NEVER logged or printed.
	store, closer, err := newStore(ctx, dsn)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "conductorctl: %v\n", err)
		os.Exit(1)
	}
	defer closer()

	// The control seam persists pause THROUGH the same store the daemon reads
	// (ADR-0011 §4): with -dsn this is the shared Postgres, so a pause set here is
	// honored by the separate daemon process. An in-process MemoryController would
	// be invisible across processes, so it is not used for the real CLI.
	a := &app{
		store: store,
		ctrl:  NewStoreController(store),
		out:   os.Stdout,
	}
	os.Exit(run(ctx, a, rest, os.Stderr))
}

// extractDSN pulls the GLOBAL -dsn/--dsn flag out of argv, returning its resolved
// value (flag value > CONDUCTOR_DSN env > "") and argv with the flag (and its
// value, when separate) removed. It is intentionally a small, position-tolerant
// pre-pass rather than a flag.FlagSet so the subcommand and its own flags survive
// untouched: a real FlagSet stops at the first non-flag token (the subcommand),
// which would make `conductorctl onboard -dsn ... repo` unparseable. Both
// `-dsn value` and `-dsn=value` (and the `--` long forms) are accepted; a `-dsn`
// with no following value is an error.
func extractDSN(argv []string) (string, []string, error) {
	dsn := os.Getenv("CONDUCTOR_DSN")
	rest := make([]string, 0, len(argv))
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch {
		case arg == "-dsn" || arg == "--dsn":
			if i+1 >= len(argv) {
				return "", nil, fmt.Errorf("flag needs an argument: %s", arg)
			}
			dsn = argv[i+1]
			i++ // consume the value too
		case strings.HasPrefix(arg, "-dsn=") || strings.HasPrefix(arg, "--dsn="):
			dsn = arg[strings.IndexByte(arg, '=')+1:]
		default:
			rest = append(rest, arg)
		}
	}
	return dsn, rest, nil
}

// newStore selects and constructs the StateStore backend from dsn, mirroring the
// daemon's newStore (cmd/conductor): empty selects the in-memory store (unchanged
// Faz-1a behavior), non-empty opens the shared Postgres store and migrates it on
// start so a fresh database is schema-ready. The returned closer releases the
// Postgres pool and is a no-op for memory, so callers can defer it
// unconditionally. The DSN (which carries the password) is NEVER logged.
func newStore(ctx context.Context, dsn string) (statestore.StateStore, func(), error) {
	if dsn == "" {
		return statestore.NewMemoryStore(), func() {}, nil
	}

	pg, err := statestore.NewPostgresStore(ctx, dsn)
	if err != nil {
		// pgx does not echo the password in its error; we still never log the DSN.
		return nil, nil, fmt.Errorf("open postgres store: %w", err)
	}
	if err := pg.Migrate(ctx); err != nil {
		pg.Close()
		return nil, nil, fmt.Errorf("migrate postgres store: %w", err)
	}
	return pg, pg.Close, nil
}

// run dispatches argv to a subcommand handler and returns the process exit code.
// It is split from main so it is unit-testable: tests inject a store/controller
// and assert the exit code and output without spawning a process.
func run(ctx context.Context, a *app, argv []string, stderr io.Writer) int {
	if len(argv) == 0 {
		_, _ = fmt.Fprint(stderr, usage())
		return 2
	}
	cmd, rest := argv[0], argv[1:]
	switch cmd {
	case "onboard":
		return runOnboard(ctx, a, rest, stderr)
	case "intake":
		return runIntake(ctx, a, rest, stderr)
	case "status":
		return runStatus(ctx, a, rest, stderr)
	case "pause":
		return runPauseResume(ctx, a, rest, stderr, false)
	case "resume":
		return runPauseResume(ctx, a, rest, stderr, true)
	case "-h", "--help", "help":
		_, _ = fmt.Fprint(a.out, usage())
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "conductorctl: unknown subcommand %q\n\n%s", cmd, usage())
		return 2
	}
}

// runOnboard parses the onboard flags and registers (or returns) the project.
func runOnboard(ctx context.Context, a *app, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("onboard", flag.ContinueOnError)
	fs.SetOutput(stderr)
	base := fs.String("base", defaultBaseBranch, "integration base branch")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: conductorctl onboard [--base <branch>] <repo>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	p, err := a.onboard(ctx, fs.Arg(0), *base)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "conductorctl: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(a.out, "onboarded %s (id=%s base=%s)\n", p.Repo, p.ID, p.BaseBranch)
	return 0
}

// runIntake parses the intake flags and ingests the scenario into the ledger.
func runIntake(ctx context.Context, a *app, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("intake", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "project id the scenario belongs to")
	file := fs.String("file", "", "path to the scenario YAML")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: conductorctl intake --project <id> --file <scenario.yaml>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *file == "" {
		fs.Usage()
		return 2
	}
	res, err := a.intake(ctx, *project, *file)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "conductorctl: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(a.out, "intake: %d scenario(s) ingested, %d skipped (already present)\n",
		len(res.Created), len(res.Skipped))
	if len(res.Created) > 0 {
		_, _ = fmt.Fprintf(a.out, "  created: %s\n", strings.Join(res.Created, ", "))
	}
	if len(res.Skipped) > 0 {
		_, _ = fmt.Fprintf(a.out, "  skipped: %s\n", strings.Join(res.Skipped, ", "))
	}
	return 0
}

// runStatus parses the status flags and renders the ledger.
func runStatus(ctx context.Context, a *app, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "project id to summarize")
	asJSON := fs.Bool("json", false, "emit machine-readable JSON")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "usage: conductorctl status --project <id> [--json]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *project == "" {
		fs.Usage()
		return 2
	}
	if err := a.status(ctx, *project, *asJSON); err != nil {
		_, _ = fmt.Fprintf(stderr, "conductorctl: %v\n", err)
		return 1
	}
	return 0
}

// runPauseResume parses the shared pause/resume flags and toggles run state.
func runPauseResume(ctx context.Context, a *app, args []string, stderr io.Writer, resume bool) int {
	name := "pause"
	if resume {
		name = "resume"
	}
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "project id to toggle")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: conductorctl %s --project <id>\n", name)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *project == "" {
		fs.Usage()
		return 2
	}
	var err error
	if resume {
		err = a.resume(ctx, *project)
	} else {
		err = a.pause(ctx, *project)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "conductorctl: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(a.out, "%sd %s\n", name, *project)
	return 0
}

// usage is the top-level help text listing the subcommands.
func usage() string {
	var b strings.Builder
	b.WriteString("conductorctl — Faz-1a operator client\n\n")
	b.WriteString("usage: conductorctl [-dsn <postgres-dsn>] <command> [flags]\n\n")
	b.WriteString("global flags:\n")
	b.WriteString("  -dsn  Postgres connection string (or CONDUCTOR_DSN); empty = in-memory.\n")
	b.WriteString("        Non-empty points at the SAME shared store the daemon uses. Never logged.\n\n")
	b.WriteString("commands:\n")
	b.WriteString("  onboard  <repo>   register a project (--base)\n")
	b.WriteString("  intake            ingest a scenario into the ledger (--project --file)\n")
	b.WriteString("  status            print the ledger summary (--project [--json])\n")
	b.WriteString("  pause             pause a project's loop (--project)\n")
	b.WriteString("  resume            resume a project's loop (--project)\n")
	return b.String()
}

// projectIDForRepo derives a stable project ID from a repo string so re-onboard is
// idempotent. It slugs the repo's last path segment, falling back to the raw repo.
func projectIDForRepo(repo string) string {
	trimmed := strings.TrimSuffix(repo, ".git")
	trimmed = strings.TrimSuffix(trimmed, "/")
	if i := strings.LastIndexAny(trimmed, "/:"); i >= 0 && i+1 < len(trimmed) {
		trimmed = trimmed[i+1:]
	}
	if trimmed == "" {
		return repo
	}
	return trimmed
}
