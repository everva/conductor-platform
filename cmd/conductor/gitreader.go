// The reconcile job's real GitReader (ADR-0016, 2B-5): it derives observed truth
// from git for the deterministic task reconcile, reading the EXACT `[task:<id>]`
// trailers the GitMerger writes on each squash-merge (ADR-0004/0010). It is the
// production counterpart to the fake GitReader the reconcile/conductor tests use,
// and the missing piece that lets `conductor -reconcile` mark merged tasks done
// from git (not just reap leases). It carries NO LLM and shells out to `git log`
// directly, mirroring the merger's git invocation (committer identity, no prompts).
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/everva/conductor-platform/internal/reconcile"
	"github.com/everva/conductor-platform/internal/statestore"
)

// cloneGitReader reads a project's base-branch commits from the per-project CLONE
// the daemon merges into (rootDir/clones/<projectID>), the same layout the daemon
// wires the GitMerger with. It is injected with the workspace root so it does not
// duplicate the provisioner's layout knowledge beyond the well-known clones subdir.
type cloneGitReader struct {
	// rootDir is the workspace root under which per-project clones live, matching
	// the daemon's `-root`. The clone for a project is rootDir/clones/<projectID>.
	// An empty rootDir leaves clones unreachable, so BaseCommits returns none (the
	// trailer reconcile then cleanly no-ops; the lease reaper is unaffected).
	rootDir string
}

// Compile-time assertion that *cloneGitReader satisfies the reconcile.GitReader seam.
var _ reconcile.GitReader = (*cloneGitReader)(nil)

// newCloneGitReader returns a GitReader rooted at the workspace root (the daemon's
// -root). The per-project clone path mirrors the provisioner/merger layout.
func newCloneGitReader(rootDir string) *cloneGitReader {
	return &cloneGitReader{rootDir: rootDir}
}

// BaseCommits returns the commits on the project's base branch in its clone, each
// with its trailer lines surfaced verbatim so the reconciler can match an EXACT
// `[task:<id>]` trailer (the reconciler does the matching, never this reader).
//
// It is conservative on a missing/unreachable clone: when no rootDir is configured,
// or the clone directory does not exist, it returns no commits and no error so the
// reconcile pass cleanly no-ops on trailer reconcile (the lease reaper, which reads
// only the store, is unaffected). A genuine git failure on an existing repo IS
// surfaced as an error so the CronJob fails-loud rather than silently skipping a
// merged task.
func (r *cloneGitReader) BaseCommits(ctx context.Context, project statestore.Project) ([]reconcile.Commit, error) {
	if r.rootDir == "" {
		return nil, nil
	}
	base := project.BaseBranch
	if base == "" {
		base = defaultBaseBranch
	}
	clone := r.rootDir + "/clones/" + project.ID
	if fi, err := os.Stat(clone); err != nil || !fi.IsDir() {
		// No clone on this host (e.g. the reconcile job runs without the daemon's
		// workspace): nothing to derive from git, not an error.
		return nil, nil
	}

	// One record per commit. %H is the SHA, %s the subject; the body holds the
	// trailers the merger wrote (`[task:<id>]`). %x1f (unit sep) separates fields and
	// %x1e (record sep) separates commits, so subjects/bodies with newlines never
	// corrupt the parse. --no-color/-c keep output deterministic.
	const (
		fieldSep  = "\x1f"
		recordSep = "\x1e"
	)
	format := "%H" + fieldSep + "%s" + fieldSep + "%b" + recordSep
	out, err := gitLogOut(ctx, clone, "log", "--no-color", "--pretty=format:"+format, base)
	if err != nil {
		return nil, fmt.Errorf("reconcile git reader: log %q in %q: %w", base, project.ID, err)
	}

	var commits []reconcile.Commit
	for _, rec := range strings.Split(out, recordSep) {
		rec = strings.Trim(rec, "\n")
		if rec == "" {
			continue
		}
		parts := strings.SplitN(rec, fieldSep, 3)
		if len(parts) < 2 {
			continue
		}
		sha := strings.TrimSpace(parts[0])
		subject := strings.TrimSpace(parts[1])
		var trailers []string
		if len(parts) == 3 {
			trailers = parseTrailerLines(parts[2])
		}
		commits = append(commits, reconcile.Commit{
			SHA:      sha,
			Subject:  subject,
			Trailers: trailers,
		})
	}
	return commits, nil
}

// parseTrailerLines extracts trailer-shaped lines from a commit body: each
// non-empty trimmed line is surfaced verbatim as a candidate trailer. The
// reconciler matches the EXACT `[task:<id>]` shape itself (ADR-0004), so this only
// has to split the body into lines and drop blanks — it never matches or rewrites.
func parseTrailerLines(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

// gitLogOut runs a read-only git subcommand in dir and returns its stdout. It uses
// the same non-interactive, identity-pinned environment the merger uses so the read
// never prompts and is deterministic in CI; failures are wrapped with the argv.
func gitLogOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %v: %w", args, err)
	}
	return string(out), nil
}
