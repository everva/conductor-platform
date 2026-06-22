package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/everva/conductor-platform/internal/agentclient"
	"github.com/everva/conductor-platform/internal/conductor"
	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/provisioner"
	"github.com/everva/conductor-platform/internal/statestore"
	"github.com/everva/conductor-platform/internal/verify"
)

// ExecutorConfig configures the real Executor (the develop→verify→merge half). These
// come from the host-agent's flags/env on davinci.
type ExecutorConfig struct {
	// RootDir is the agent's writable workspace root (clones + worktrees).
	RootDir string
	// Repo is the git remote URL of the project (e.g. https://github.com/everva/optiway.git).
	Repo string
	// BaseBranch is the branch conductor work merges onto (e.g. conductor/optiway).
	// optiway's main is NEVER touched.
	BaseBranch string
	// GHToken authenticates git clone + push of the (private) repo. It is installed as
	// a credential helper by the provisioner and stripped from the performer subprocess
	// env by envsafe — the performer never sees it.
	GHToken string
	// DevelopCmd is the performer argv (default ["claude","-p"]).
	DevelopCmd []string
	// Timeout bounds a single develop subprocess.
	Timeout time.Duration
	// Gates are the deterministic verify gates (build/test/lint) — the sole merge
	// authority (Rule#9). Without gates the verifier has nothing to check.
	Gates []verify.Gate
	// Push enables pushing the merged base to the remote (so the director sees the
	// conductor/optiway branch update). Default true for a real host.
	Push bool
	// PushRemote is the git remote to push to (default "origin").
	PushRemote string
}

// noopHoldout is a HoldoutStore that injects nothing — used until the gateway serves
// holdouts (Faz G3). The verifier still runs the real gates; only the ADR-0018 hidden
// holdout is absent.
type noopHoldout struct{}

func (noopHoldout) Fetch(_ context.Context, _ string) (verify.Holdout, error) {
	return verify.Holdout{}, nil
}

// RealExecutor wires the proven execution packages (provisioner + engine + verify +
// merger) behind the Executor seam. It runs one task at a time and holds that task's
// worktree between Run and Merge.
type RealExecutor struct {
	prov   *provisioner.Provisioner
	eng    *engine.CommandEngine
	verf   *verify.Verifier
	merger *conductor.GitMerger
	cfg    ExecutorConfig

	// ws holds the live worktree per in-flight task (single task at a time; map for safety).
	ws map[string]engine.Workspace
}

// NewRealExecutor builds the executor from config, mirroring the daemon's wiring
// (cmd/conductor) but local-to-this-host. A non-nil error means a misconfiguration
// (e.g. an unwritable RootDir).
func NewRealExecutor(cfg ExecutorConfig) (*RealExecutor, error) {
	if cfg.RootDir == "" {
		return nil, fmt.Errorf("agent executor: RootDir is required")
	}
	if len(cfg.DevelopCmd) == 0 {
		cfg.DevelopCmd = []string{"claude", "-p"}
	}
	if cfg.PushRemote == "" {
		cfg.PushRemote = "origin"
	}
	prov, err := provisioner.New(provisioner.Config{RootDir: cfg.RootDir, GHToken: cfg.GHToken})
	if err != nil {
		return nil, fmt.Errorf("agent executor: provisioner: %w", err)
	}
	eng := engine.NewCommandEngine(engine.RecipeConfig{DevelopCmd: cfg.DevelopCmd, Timeout: cfg.Timeout})
	verf := verify.New(noopHoldout{}, verify.Config{})
	merger := conductor.NewGitMerger(
		func(projectID string) string { return cfg.RootDir + "/clones/" + projectID },
		conductor.WithPush(conductor.PushConfig{Enabled: cfg.Push, Remote: cfg.PushRemote, GHToken: cfg.GHToken}),
	)
	return &RealExecutor{prov: prov, eng: eng, verf: verf, merger: merger, cfg: cfg, ws: map[string]engine.Workspace{}}, nil
}

// project rebuilds the statestore.Project the execution packages consume from the
// agent's config (the gateway holds the authoritative record; the agent only needs
// repo + base to clone/worktree/merge).
func (e *RealExecutor) project(projectID string) statestore.Project {
	return statestore.Project{ID: projectID, Repo: e.cfg.Repo, BaseBranch: e.cfg.BaseBranch}
}

// stateTask maps the gateway's TaskInfo into the statestore.Task the packages consume.
func stateTask(t agentclient.TaskInfo) statestore.Task {
	return statestore.Task{
		ID: t.ID, ProjectID: t.ProjectID, Lane: t.Lane, Tier: t.Tier,
		Requires: t.Requires, Deps: t.Deps, Branch: t.Branch, ScenarioID: t.ScenarioID,
	}
}

// Run provisions a worktree, develops (performer), and verifies (gate). The worktree
// is held for a later Merge.
func (e *RealExecutor) Run(ctx context.Context, taskInfo agentclient.TaskInfo, _ agentclient.ScenarioInfo) (RunOutcome, error) {
	project := e.project(taskInfo.ProjectID)
	task := stateTask(taskInfo)

	ws, err := e.prov.Workspace(ctx, project, task)
	if err != nil {
		return RunOutcome{}, fmt.Errorf("provision workspace: %w", err)
	}
	e.ws[task.ID] = ws

	verdict, err := e.eng.Develop(ctx, task, ws)
	if err != nil {
		return RunOutcome{}, fmt.Errorf("develop: %w", err)
	}

	review, checks, err := e.verf.Verify(ctx, verdict, ws, e.cfg.Gates, "")
	if err != nil {
		return RunOutcome{}, fmt.Errorf("verify: %w", err)
	}

	return RunOutcome{
		Result:  review.Result,
		Branch:  ws.Branch,
		Summary: review.Summary,
		Checks:  toChecks(checks),
	}, nil
}

// Merge squash-merges the verified branch. For an APPROVED held task it re-attaches
// the branch if needed, merges the current base in (drift guard), and re-verifies
// before merging — mirroring the daemon's mergeApproved (never merge stale work).
func (e *RealExecutor) Merge(ctx context.Context, taskInfo agentclient.TaskInfo, branch string, approved bool) (string, error) {
	project := e.project(taskInfo.ProjectID)
	task := stateTask(taskInfo)

	ws, ok := e.ws[task.ID]
	if !ok {
		// Worktree gone (e.g. a fresh agent run after a restart): re-attach the branch.
		w, err := e.prov.WorkspaceForBranch(ctx, project, task, branch)
		if err != nil {
			return "", fmt.Errorf("re-attach branch %q: %w", branch, err)
		}
		ws = w
		e.ws[task.ID] = ws
	}

	if approved {
		// Drift guard: merge the current base into the held branch, then re-run the
		// cheap gate. If the base drifted enough to conflict or break the gate, refuse.
		if err := e.prov.MergeBaseIntoWorktree(ctx, project, ws); err != nil {
			if e.prov.IsBaseMergeConflict(err) {
				return "", fmt.Errorf("approved merge refused: base drifted into a conflict")
			}
			return "", fmt.Errorf("merge base into held branch: %w", err)
		}
		review, _, err := e.verf.Verify(ctx, engine.Verdict{}, ws, e.cfg.Gates, "")
		if err != nil {
			return "", fmt.Errorf("re-verify after approval: %w", err)
		}
		if review.Result != "pass" {
			return "", fmt.Errorf("approved merge refused: re-verify failed (base drift broke the gate)")
		}
	}

	sha, err := e.merger.SquashMerge(ctx, project, task, ws)
	if err != nil {
		return "", fmt.Errorf("squash-merge: %w", err)
	}
	return sha, nil
}

// Cleanup removes the task's worktree (best-effort).
func (e *RealExecutor) Cleanup(ctx context.Context, taskInfo agentclient.TaskInfo) {
	ws, ok := e.ws[taskInfo.ID]
	if !ok {
		return
	}
	_ = e.prov.Cleanup(ctx, ws)
	delete(e.ws, taskInfo.ID)
}

// toChecks maps engine.Check (the gate evidence) onto the wire Check shape.
func toChecks(checks []engine.Check) []agentclient.Check {
	out := make([]agentclient.Check, 0, len(checks))
	for _, c := range checks {
		out = append(out, agentclient.Check{Name: c.Name, Result: c.Result, Evidence: c.Evidence})
	}
	return out
}
