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
	// Faz-1a wiring: a single in-memory store + in-process controller. Faz-1b
	// replaces these with the Postgres-backed implementations behind the same
	// interfaces; the handlers below are unaffected.
	a := &app{
		store: statestore.NewMemoryStore(),
		ctrl:  NewMemoryController(),
		out:   os.Stdout,
	}
	os.Exit(run(context.Background(), a, os.Args[1:], os.Stderr))
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
	scenario, task, err := a.intake(ctx, *project, *file)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "conductorctl: %v\n", err)
		return 1
	}
	_, _ = fmt.Fprintf(a.out, "intake %s (lane=%s tier=%s status=%s)\n", scenario.ID, task.Lane, task.Tier, task.Status)
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
	b.WriteString("usage: conductorctl <command> [flags]\n\n")
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
