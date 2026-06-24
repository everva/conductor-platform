package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/everva/conductor-platform/internal/agentclient"
	"github.com/everva/conductor-platform/internal/envsafe"
)

// Intake ENHANCE (agent side, code-aware). The director writes a rough request in intake; the
// gateway records a pending job; THIS agent — which has the project's repo cloned + a logged-in
// claude — claims it, runs claude read-only over the real code, and writes back a detailed
// Turkish spec. It is a separate, lightweight loop from the task Runner (no lease, no worktree
// mutation, no merge): a pure read → reason → return.

// enhancePrompt instructs claude to explore the checkout and emit a detailed TURKISH spec. The
// rough request is appended after it on stdin.
const enhancePrompt = `You are a senior engineer assisting the conductor intake. Your CURRENT WORKING DIRECTORY is a checkout of the project's codebase. The director wrote a ROUGH work request (below), likely in Turkish.

Do this:
1. EXPLORE the codebase EFFICIENTLY to understand EXACTLY what the request touches. Use grep/ripgrep to locate the feature BY NAME, then read the directly-relevant files. Be fast and targeted, not exhaustive. Identify real file paths, module / model / table / component names, and the footprint across every relevant layer (DB schema + migrations, API, web/UI, shared packages, i18n, tests, seed, generated clients, OpenAPI contracts).
2. COMPLETENESS (critical): before writing, run a grep for EVERY identifier the change touches (the feature name AND each field/column/symbol you plan to remove or change, e.g. a column like serviceCompanyId) and ACCOUNT FOR EVERY MATCHING FILE in the spec. Do NOT omit TEST files — unit/integration/e2e specs, fixtures, mocks — nor seeds, generated API clients, or OpenAPI/i18n. A file that references a symbol you remove WILL break the build/tests, so it MUST appear in your per-layer change list. If an acceptance criterion says "build green" / "tests green", the body must list every edit needed to make that true — the spec must be internally consistent (no criterion the listed edits don't actually satisfy).
3. Produce a DETAILED, CLEAN, well-structured specification, GROUNDED in the actual code: state concretely, per layer, what to add / change / remove using the REAL names you found, plus clear, verifiable acceptance criteria.

Work promptly — aim to finish within a few minutes; favor a focused, COMPLETE, correct spec.

STRICT OUTPUT RULES:
- Write the ENTIRE specification in TURKISH (Türkçe) — clear, detailed and readable for the director.
- Treat it as ONE atomic piece of work; do NOT split it into separate independent tasks.
- Output ONLY the enhanced spec text. No preamble, no markdown code fences, no JSON, no "Here is…".
- Do NOT modify, create, or delete ANY file. Read only.

ROUGH REQUEST:
`

// enhanceTimeout bounds a single enhance run (code exploration + write). Generous: the
// completeness pass (grep every touched identifier across a large monorepo + compose) can take
// 10-15 min on a thorough run, so the ceiling is high enough that a genuinely-working enhance is
// never killed mid-flight (the earlier 12m ceiling occasionally cut off a still-working run).
const enhanceTimeout = 18 * time.Minute

// runEnhanceClaude runs `claude -p --dangerously-skip-permissions` with cwd=dir (so claude's
// file tools read the real code) over enhancePrompt+roughSpec, returning the produced Turkish
// spec (stdout). It relies on the host's interactive claude login (env-inherited, sanitized to
// strip CONDUCTOR_*/GH_TOKEN). --dangerously-skip-permissions lets it read/grep without prompts;
// the prompt forbids writes and the checkout is a throwaway read-only worktree anyway.
func runEnhanceClaude(ctx context.Context, dir, roughSpec string) (string, error) {
	if _, err := exec.LookPath("claude"); err != nil {
		return "", fmt.Errorf("agent enhance: claude CLI not found: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, enhanceTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "claude", "-p", "--dangerously-skip-permissions") //nolint:gosec // fixed subscription claude command; read-only enhance.
	cmd.Dir = dir
	cmd.Env = envsafe.Sanitize(os.Environ())
	cmd.Stdin = bytes.NewReader([]byte(enhancePrompt + roughSpec))
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("agent enhance: claude run: %s", msg)
	}
	spec := strings.TrimSpace(out.String())
	if spec == "" {
		return "", errors.New("agent enhance: claude produced no spec")
	}
	return spec, nil
}

// EnhanceGateway is the agent-API surface the EnhanceRunner consumes. *agentclient.Client
// satisfies it; tests inject a fake.
type EnhanceGateway interface {
	ClaimEnhance(ctx context.Context, projectID string) (agentclient.EnhanceClaim, bool, error)
	CompleteEnhance(ctx context.Context, projectID, jobID, result, errMsg string) error
}

// Enhancer turns a rough request into a detailed Turkish spec by reading the project's code.
// *RealExecutor satisfies it; tests inject a fake.
type Enhancer interface {
	Enhance(ctx context.Context, projectID, roughSpec string) (string, error)
}

// EnhanceRunner polls the gateway for pending enhance jobs and runs them. It is independent of
// the task Runner so an enhance never blocks (or is blocked by) a leased task.
type EnhanceRunner struct {
	gw        EnhanceGateway
	ex        Enhancer
	projectID string
	log       *slog.Logger
	poll      time.Duration
}

// NewEnhanceRunner builds an EnhanceRunner. A zero poll defaults to 10s.
func NewEnhanceRunner(gw EnhanceGateway, ex Enhancer, projectID string, poll time.Duration, log *slog.Logger) *EnhanceRunner {
	if log == nil {
		log = slog.Default()
	}
	if poll <= 0 {
		poll = 10 * time.Second
	}
	return &EnhanceRunner{gw: gw, ex: ex, projectID: projectID, log: log, poll: poll}
}

// RunOnce claims at most one enhance job and completes it. It returns (true, _) when it handled
// a job (so the loop can poll again immediately for more), (false, nil) when none was pending.
// An enhance failure is reported back as a FAILED job (never silently dropped) — RunOnce itself
// only returns a non-nil error for a gateway/claim transport failure.
func (r *EnhanceRunner) RunOnce(ctx context.Context) (bool, error) {
	claim, ok, err := r.gw.ClaimEnhance(ctx, r.projectID)
	if err != nil {
		return false, fmt.Errorf("agent enhance: claim: %w", err)
	}
	if !ok {
		return false, nil
	}
	r.log.Info("agent enhance: running", "job", claim.ID)
	spec, eerr := r.ex.Enhance(ctx, r.projectID, claim.RoughSpec)
	if eerr != nil {
		r.log.Warn("agent enhance: failed; reporting", "job", claim.ID, "err", eerr)
		if cerr := r.gw.CompleteEnhance(ctx, r.projectID, claim.ID, "", eerr.Error()); cerr != nil {
			return true, fmt.Errorf("agent enhance: report failure: %w", cerr)
		}
		return true, nil
	}
	if cerr := r.gw.CompleteEnhance(ctx, r.projectID, claim.ID, spec, ""); cerr != nil {
		return true, fmt.Errorf("agent enhance: report result: %w", cerr)
	}
	r.log.Info("agent enhance: done", "job", claim.ID, "spec_bytes", len(spec))
	return true, nil
}

// Loop runs RunOnce repeatedly, idle-sleeping between empty polls, until ctx is cancelled.
func (r *EnhanceRunner) Loop(ctx context.Context, idle time.Duration) error {
	if idle <= 0 {
		idle = r.poll
	}
	for {
		handled, err := r.RunOnce(ctx)
		if err != nil {
			r.log.Debug("agent enhance: run-once error", "err", err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if handled {
			continue // there may be more pending; poll again immediately
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(idle):
		}
	}
}
