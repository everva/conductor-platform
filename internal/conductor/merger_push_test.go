package conductor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/events"
	"github.com/everva/conductor-platform/internal/registry"
	"github.com/everva/conductor-platform/internal/statestore"
)

// setupCloneWithBareOrigin builds a real local git topology mirroring the daemon's:
// a BARE repo acting as `origin`, a CLONE of it (where the merge happens) with the
// base branch checked out, and a task branch in the clone carrying one extra commit.
// It returns the clone path, the bare-origin path, the base branch name, and the
// task branch name. No network/token is needed (origin is a local bare repo).
func setupCloneWithBareOrigin(t *testing.T) (clone, origin, base, branch string) {
	t.Helper()
	base = "develop"
	branch = "conductor/proj-1/T-1"

	// Seed a source repo with the base branch + one commit, then make a BARE clone of
	// it to act as the shared origin.
	src := t.TempDir()
	gitT(t, src, "init", "-q", "-b", base)
	gitT(t, src, "config", "user.name", "test")
	gitT(t, src, "config", "user.email", "test@test")
	if err := os.WriteFile(filepath.Join(src, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write base: %v", err)
	}
	gitT(t, src, "add", ".")
	gitT(t, src, "commit", "-q", "-m", "base")

	origin = t.TempDir()
	gitT(t, origin, "init", "-q", "--bare", "-b", base)
	gitT(t, src, "remote", "add", "origin", origin)
	gitT(t, src, "push", "-q", "origin", base)

	// The CLONE the merger operates in: clone from the bare origin so `git push
	// origin develop` has a real upstream to advance. The bare origin's HEAD is set
	// to base (the -b above) so the clone checks base out instead of an unborn HEAD.
	cloneParent := t.TempDir()
	clone = filepath.Join(cloneParent, "clone")
	gitT(t, cloneParent, "clone", "-q", origin, clone)
	gitT(t, clone, "config", "user.name", "test")
	gitT(t, clone, "config", "user.email", "test@test")
	gitT(t, clone, "checkout", "-q", base)

	// Cut a task branch with one extra commit, mirroring the develop worktree.
	gitT(t, clone, "checkout", "-q", "-b", branch)
	if err := os.WriteFile(filepath.Join(clone, "feature.txt"), []byte("feature\n"), 0o644); err != nil {
		t.Fatalf("write feature: %v", err)
	}
	gitT(t, clone, "add", ".")
	gitT(t, clone, "commit", "-q", "-m", "feature work")
	gitT(t, clone, "checkout", "-q", base)
	return clone, origin, base, branch
}

// TestGitMerger_PushEnabled_OriginAdvances proves ADR-0022 happy path: with push
// ENABLED, after a successful squash-merge the BARE origin's base branch ALSO
// advances to the merge commit (the push propagated), and the returned error is nil.
func TestGitMerger_PushEnabled_OriginAdvances(t *testing.T) {
	ctx := context.Background()
	clone, origin, base, branch := setupCloneWithBareOrigin(t)

	originBefore := gitT(t, origin, "rev-parse", base)
	t.Logf("origin %s BEFORE merge: %s", base, originBefore)

	m := NewGitMerger(func(string) string { return clone }, WithPush(PushConfig{Enabled: true})) // Remote defaults to origin, no token (local bare).
	project := statestore.Project{ID: "proj-1", BaseBranch: base}
	task := statestore.Task{ID: "T-1", ProjectID: "proj-1"}
	ws := engine.Workspace{Path: clone, Branch: branch}

	sha, err := m.SquashMerge(ctx, project, task, ws)
	if err != nil {
		t.Fatalf("SquashMerge with push: %v", err)
	}
	if sha == "" {
		t.Fatalf("empty merge sha")
	}

	// The local clone base advanced to the merge sha with the trailer.
	if head := gitT(t, clone, "rev-parse", base); head != sha {
		t.Fatalf("clone %s HEAD %q != merge sha %q", base, head, sha)
	}
	// The BARE origin base ALSO advanced to the SAME merge sha (push propagated).
	originAfter := gitT(t, origin, "rev-parse", base)
	t.Logf("origin %s AFTER merge:  %s", base, originAfter)
	if originAfter != sha {
		t.Fatalf("origin %s did not advance to merge sha: want %q got %q", base, sha, originAfter)
	}
	if originAfter == originBefore {
		t.Fatalf("origin %s unchanged after push (want advance): %s", base, originBefore)
	}
	// The pushed commit carries the exact [task:T-1] trailer.
	body := gitT(t, origin, "log", "-1", "--format=%B", base)
	if !strings.Contains(body, "[task:T-1]") {
		t.Fatalf("pushed commit missing [task:T-1] trailer:\n%s", body)
	}
}

// TestGitMerger_PushFail_MergeStandsAndPushErrorReturned proves ADR-0022 push-fail
// semantics at the merger: a push to an UNREACHABLE remote does NOT undo the local
// merge and does NOT make the merge call return an empty sha — it returns the valid
// merge sha + a *PushError. The local base still has the merge commit + trailer
// (no silent loss, no undo).
func TestGitMerger_PushFail_MergeStandsAndPushErrorReturned(t *testing.T) {
	ctx := context.Background()
	clone, _, base, branch := setupCloneWithBareOrigin(t)

	// Point the merger at a remote that does not exist (a bogus local path), so the
	// push fails while the local merge succeeds.
	bogus := filepath.Join(t.TempDir(), "does-not-exist.git")
	m := NewGitMerger(func(string) string { return clone }, WithPush(PushConfig{Enabled: true, Remote: bogus}))
	project := statestore.Project{ID: "proj-1", BaseBranch: base}
	task := statestore.Task{ID: "T-1", ProjectID: "proj-1"}
	ws := engine.Workspace{Path: clone, Branch: branch}

	sha, err := m.SquashMerge(ctx, project, task, ws)
	if err == nil {
		t.Fatalf("expected a push error, got nil (sha %q)", sha)
	}
	var pe *PushError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *PushError, got %T: %v", err, err)
	}
	if sha == "" {
		t.Fatalf("merge sha must be valid even on push-fail (merge landed locally)")
	}
	if pe.SHA != sha {
		t.Fatalf("PushError.SHA %q != returned sha %q", pe.SHA, sha)
	}
	if pe.Remote != bogus {
		t.Fatalf("PushError.Remote %q != %q", pe.Remote, bogus)
	}

	// The LOCAL base must STILL have the merge commit + trailer (no undo, no loss).
	if head := gitT(t, clone, "rev-parse", base); head != sha {
		t.Fatalf("local %s HEAD %q != merge sha %q (merge was undone!)", base, head, sha)
	}
	body := gitT(t, clone, "log", "-1", "--format=%B", base)
	if !strings.Contains(body, "[task:T-1]") {
		t.Fatalf("local merge commit lost its [task:T-1] trailer:\n%s", body)
	}
	if _, statErr := os.Stat(filepath.Join(clone, "feature.txt")); statErr != nil {
		t.Fatalf("feature content missing from local base after push-fail: %v", statErr)
	}
}

// TestGitMerger_PushDisabled_NoRemoteContact proves the default (push OFF) is
// byte-identical to the local-only model: the merge succeeds, returns a valid sha
// with NO error, and the origin is NOT touched even though one exists.
func TestGitMerger_PushDisabled_NoRemoteContact(t *testing.T) {
	ctx := context.Background()
	clone, origin, base, branch := setupCloneWithBareOrigin(t)
	originBefore := gitT(t, origin, "rev-parse", base)

	m := NewGitMerger(func(string) string { return clone }) // No WithPush => disabled.
	project := statestore.Project{ID: "proj-1", BaseBranch: base}
	task := statestore.Task{ID: "T-1", ProjectID: "proj-1"}
	ws := engine.Workspace{Path: clone, Branch: branch}

	sha, err := m.SquashMerge(ctx, project, task, ws)
	if err != nil {
		t.Fatalf("SquashMerge (push off): %v", err)
	}
	if sha == "" {
		t.Fatalf("empty merge sha")
	}
	// Origin must be UNCHANGED: push disabled never contacts it.
	if originAfter := gitT(t, origin, "rev-parse", base); originAfter != originBefore {
		t.Fatalf("origin advanced with push disabled: before %q after %q", originBefore, originAfter)
	}
}

// TestGitMerger_PushDisabled_WithDisabledConfigEqualsNoOption proves WithPush of a
// DISABLED config is identical to passing no option (Enabled false => no push).
func TestGitMerger_PushDisabled_WithDisabledConfigEqualsNoOption(t *testing.T) {
	ctx := context.Background()
	clone, origin, base, branch := setupCloneWithBareOrigin(t)
	originBefore := gitT(t, origin, "rev-parse", base)

	m := NewGitMerger(func(string) string { return clone }, WithPush(PushConfig{Enabled: false, Remote: "origin"}))
	sha, err := m.SquashMerge(ctx,
		statestore.Project{ID: "proj-1", BaseBranch: base},
		statestore.Task{ID: "T-1", ProjectID: "proj-1"},
		engine.Workspace{Path: clone, Branch: branch})
	if err != nil {
		t.Fatalf("SquashMerge: %v", err)
	}
	if sha == "" {
		t.Fatalf("empty sha")
	}
	if originAfter := gitT(t, origin, "rev-parse", base); originAfter != originBefore {
		t.Fatalf("origin advanced with disabled config: before %q after %q", originBefore, originAfter)
	}
}

// TestGitMerger_PushTokenRedactedInError unit-tests the gh-token credential wiring's
// redaction WITHOUT a real token: when a token is configured and the push fails, the
// returned (Push)error must NOT contain the token literal. The token is delivered via
// the credential helper, so a bogus remote that fails still proves the redaction path.
func TestGitMerger_PushTokenRedactedInError(t *testing.T) {
	ctx := context.Background()
	clone, _, base, branch := setupCloneWithBareOrigin(t)

	const fakeToken = "ghp_SECRET_TOKEN_DO_NOT_LEAK_1234567890"
	bogus := filepath.Join(t.TempDir(), "missing.git")
	m := NewGitMerger(func(string) string { return clone },
		WithPush(PushConfig{Enabled: true, Remote: bogus, GHToken: fakeToken}))

	sha, err := m.SquashMerge(ctx,
		statestore.Project{ID: "proj-1", BaseBranch: base},
		statestore.Task{ID: "T-1", ProjectID: "proj-1"},
		engine.Workspace{Path: clone, Branch: branch})
	if err == nil {
		t.Fatalf("expected push error, got nil")
	}
	if sha == "" {
		t.Fatalf("merge sha must be valid on push-fail")
	}
	// The primary guarantee: the token literal NEVER appears in the surfaced error,
	// regardless of which git step failed.
	if strings.Contains(err.Error(), fakeToken) {
		t.Fatalf("gh-token LEAKED into error: %v", err)
	}
}

// TestRedactPush_ReplacesTokenLiteral unit-tests the redaction helper directly: a
// crafted error containing the token literal is rewritten so the token is replaced
// by the REDACTED marker (the case where git DOES echo the token, e.g. in a URL).
func TestRedactPush_ReplacesTokenLiteral(t *testing.T) {
	const tok = "ghp_LEAKY_TOKEN_abcdef123456"
	m := NewGitMerger(nil, WithPush(PushConfig{Enabled: true, GHToken: tok}))
	in := errors.New("git push: https://x-access-token:" + tok + "@github.com/acme/repo failed")
	out := m.redactPush(in)
	if strings.Contains(out.Error(), tok) {
		t.Fatalf("token not redacted: %v", out)
	}
	if !strings.Contains(out.Error(), "REDACTED") {
		t.Fatalf("expected REDACTED marker: %v", out)
	}
	// With no token, redactPush is a passthrough.
	m2 := NewGitMerger(nil)
	if got := m2.redactPush(in); got != in {
		t.Fatalf("redactPush with no token should pass through unchanged")
	}
}

// TestTick_PushFailed_TaskDoneAndEventEmitted proves the tick-level ADR-0022
// contract: when the merger reports merged-locally-but-push-failed (*PushError), the
// tick keeps the task DONE (OutcomeMerged + the valid merge sha) and emits a
// push-failed intervention-needed event — never blocking, never silent.
func TestTick_PushFailed_TaskDoneAndEventEmitted(t *testing.T) {
	ctx := context.Background()
	store := statestore.NewMemoryStore()
	if err := store.CreateProject(ctx, statestore.Project{ID: projectID, Repo: "owner/repo", BaseBranch: "develop"}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := store.CreateTask(ctx, statestore.Task{ID: "T-1", ProjectID: projectID, Lane: "x", Tier: "T2", Status: registry.StatusReady, Branch: "conductor/T-1"}); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	em := &recordingEmitter{}
	pushErr := &PushError{SHA: "cafef00d", Remote: "origin", Err: errors.New("network unreachable")}
	cond, err := New(Deps{
		Store:       store,
		Picker:      registry.NewRegistry(store),
		Provisioner: &fakeProvisioner{},
		Engine:      &fakeEngine{verdict: engine.Verdict{Result: "pass"}},
		Verifier:    &fakeVerifier{result: "pass"},
		Merger:      &fakeMerger{sha: "cafef00d", err: pushErr},
		HostID:      "host-1",
		Emitter:     em,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	// The task is DONE (merged locally), not blocked.
	if res.Outcome != OutcomeMerged {
		t.Fatalf("expected merged (done) on push-fail, got %s", res.Outcome)
	}
	if res.MergeSHA != "cafef00d" {
		t.Fatalf("expected merge sha cafef00d, got %q", res.MergeSHA)
	}
	got, err := store.GetTask(ctx, "T-1")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if got.Status != statusDone {
		t.Fatalf("task must be done after push-fail (merged locally), got %q", got.Status)
	}

	// A push-failed event was emitted (reuse of the intervention-needed kind, merge
	// phase), carrying the merge sha + reason — operator visibility, never silent.
	em.mu.Lock()
	defer em.mu.Unlock()
	var found bool
	for _, e := range em.evts {
		if e.Phase == events.PhaseMerge && e.Kind == events.KindInterventionNeeded {
			if e.Payload["reason"] != "push-failed" {
				t.Errorf("push-failed event has wrong reason: %v", e.Payload["reason"])
			}
			if e.Payload["merge_sha"] != "cafef00d" {
				t.Errorf("push-failed event missing merge_sha: %v", e.Payload)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("no push-failed (merge/intervention-needed) event emitted; kinds=%v", em.kinds())
	}
}
