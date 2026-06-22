// conductor-agent is the gateway-mediated performer host-agent (ADR-0048, Faz A): it
// leases tasks from the conductor-api gateway over HTTP, develops + verifies them
// locally (reusing the same engine/verify/merger packages as the daemon), and — under
// held-for-review — merges only after the director approves. It NEVER opens Postgres;
// the gateway owns all state. Run one per performer host (e.g. davinci).
//
// Secrets: the gateway bearer token and the git (gh) token come from ENV, never flags
// (so they are not in the process table). The git token is installed as a credential
// helper by the provisioner and stripped from the performer subprocess env by envsafe.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/everva/conductor-platform/internal/agent"
	"github.com/everva/conductor-platform/internal/agentclient"
	"github.com/everva/conductor-platform/internal/scaffolder"
	"github.com/everva/conductor-platform/internal/verify"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "conductor-agent:", err)
		os.Exit(1)
	}
}

type config struct {
	gateway      string
	project      string
	hostID       string
	capabilities []string
	root         string
	repo         string
	base         string
	recipeDir    string
	developCmd   []string
	timeout      time.Duration
	interval     time.Duration
	poll         time.Duration
	noPush       bool
	pushRemote   string
}

func run() error {
	var (
		gateway    = flag.String("gateway", "", "conductor-api gateway base URL (required), e.g. http://conductor-api:8080")
		project    = flag.String("project", "", "project ID to work (required)")
		hostID     = flag.String("host-id", defaultHostID(), "stable host identifier")
		caps       = flag.String("capabilities", "", "comma-separated host capabilities for routing (e.g. linux,backend)")
		root       = flag.String("root", defaultRoot(), "writable workspace root (clones + worktrees)")
		repo       = flag.String("repo", "", "git remote URL of the project (required), e.g. https://github.com/everva/optiway.git")
		base       = flag.String("base", "", "base branch conductor work merges onto (required), e.g. conductor/optiway")
		recipeDir  = flag.String("recipe-dir", "", "directory containing .conductor/config.yaml (develop cmd + verify gates)")
		developCmd = flag.String("develop-cmd", "", "override the performer argv (default from recipe, else 'claude -p')")
		timeout    = flag.Duration("timeout", 30*time.Minute, "per-develop subprocess timeout")
		interval   = flag.Duration("interval", 10*time.Second, "idle poll interval when there is no work")
		poll       = flag.Duration("poll", 15*time.Second, "held-task approval poll interval")
		noPush     = flag.Bool("no-push", false, "do NOT push the merged base to the remote (local-only)")
		pushRemote = flag.String("push-remote", "origin", "git remote to push the merged base to")
	)
	flag.Parse()

	cfg := config{
		gateway: strings.TrimSpace(*gateway), project: strings.TrimSpace(*project),
		hostID: strings.TrimSpace(*hostID), capabilities: splitCSV(*caps),
		root: *root, repo: strings.TrimSpace(*repo), base: strings.TrimSpace(*base),
		recipeDir: *recipeDir, developCmd: splitFields(*developCmd),
		timeout: *timeout, interval: *interval, poll: *poll, noPush: *noPush, pushRemote: *pushRemote,
	}
	if cfg.gateway == "" || cfg.project == "" || cfg.repo == "" || cfg.base == "" {
		return errors.New("-gateway, -project, -repo and -base are required")
	}

	token := os.Getenv("CONDUCTOR_AGENT_TOKEN")
	if token == "" {
		return errors.New("CONDUCTOR_AGENT_TOKEN env is required (the gateway bearer token)")
	}
	ghToken := firstEnv("CONDUCTOR_GH_TOKEN", "GH_TOKEN")

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Resolve the recipe: develop cmd + verify gates. The gate is the SOLE merge
	// authority (Rule#9), so a recipe with gates is required unless -develop-cmd plus
	// no gates is explicitly intended (which would verify nothing — rejected below).
	devArgv, gates, err := resolveRecipe(cfg, logger)
	if err != nil {
		return err
	}

	exec, err := agent.NewRealExecutor(agent.ExecutorConfig{
		RootDir:    cfg.root,
		Repo:       cfg.repo,
		BaseBranch: cfg.base,
		GHToken:    ghToken,
		DevelopCmd: devArgv,
		Timeout:    cfg.timeout,
		Gates:      gates,
		Push:       !cfg.noPush,
		PushRemote: cfg.pushRemote,
	})
	if err != nil {
		return err
	}

	client := agentclient.New(cfg.gateway, token)
	runner := agent.New(client, exec, agent.Config{
		ProjectID: cfg.project, HostID: cfg.hostID, Capabilities: cfg.capabilities,
		PollInterval: cfg.poll, Logger: logger,
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Info("conductor-agent: starting",
		"gateway", cfg.gateway, "project", cfg.project, "host", cfg.hostID,
		"capabilities", cfg.capabilities, "repo", cfg.repo, "base", cfg.base, "push", !cfg.noPush)

	if err := runner.Loop(ctx, cfg.interval); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	logger.Info("conductor-agent: stopped")
	return nil
}

// resolveRecipe loads the develop cmd + verify gates from -recipe-dir (.conductor/
// config.yaml), with -develop-cmd overriding the develop argv. A recipe with no gates
// is rejected: the verifier would have nothing to check, which would be a fake-green.
func resolveRecipe(cfg config, logger *slog.Logger) ([]string, []verify.Gate, error) {
	developCmd := cfg.developCmd
	var gates []verify.Gate

	if cfg.recipeDir != "" {
		recipe, err := scaffolder.LoadRecipe(cfg.recipeDir)
		if err != nil {
			return nil, nil, fmt.Errorf("load recipe from %q: %w", cfg.recipeDir, err)
		}
		if len(developCmd) == 0 {
			developCmd = recipe.Develop
		}
		for _, g := range recipe.Gates {
			gates = append(gates, verify.Gate{Name: g.Name, Argv: g.Argv})
		}
		logger.Info("conductor-agent: recipe loaded", "dir", cfg.recipeDir, "gates", len(gates))
	}

	if len(developCmd) == 0 {
		developCmd = []string{"claude", "-p"}
	}
	if len(gates) == 0 {
		return nil, nil, errors.New("no verify gates configured: provide -recipe-dir with a .conductor/config.yaml that declares gates (the gate is the sole merge authority)")
	}
	return developCmd, gates, nil
}

func defaultHostID() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "conductor-agent"
}

func defaultRoot() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h + "/.conductor-agent"
	}
	return "/tmp/conductor-agent"
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func splitFields(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.Fields(s)
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}
