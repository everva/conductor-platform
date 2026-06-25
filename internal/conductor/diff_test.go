package conductor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/events"
	"github.com/everva/conductor-platform/internal/governance"
	"github.com/everva/conductor-platform/internal/registry"
	"github.com/everva/conductor-platform/internal/statestore"
)

// fakeDiffer is a scripted Differ: it returns a fixed DiffSummary (or an error) so
// the tick's diff-on-green emit can be driven without a real git worktree. It
// records the workspace it was asked to diff so a test can assert it ran against
// the verified branch.
type fakeDiffer struct {
	summary events.DiffSummary
	err     error
	calls   int
	gotWS   engine.Workspace
}

func (f *fakeDiffer) Diff(_ context.Context, _ statestore.Project, ws engine.Workspace) (events.DiffSummary, error) {
	f.calls++
	f.gotWS = ws
	if f.err != nil {
		return events.DiffSummary{}, f.err
	}
	return f.summary, nil
}

// condWithDiffer builds a conductor wired with the given emitter + differ + an
// optional policy, over the shared fakes, so the diff-on-green emit can be asserted
// on both the auto-merge and held paths.
func condWithDiffer(t *testing.T, em Emitter, differ Differ, policy Policy) (*Conductor, *statestore.MemoryStore, *fakeMerger) {
	t.Helper()
	ctx := context.Background()
	store := statestore.NewMemoryStore()
	if err := store.CreateProject(ctx, statestore.Project{ID: projectID, Repo: "owner/repo", BaseBranch: "develop"}); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	if err := store.CreateTask(ctx, statestore.Task{ID: "T-1", ProjectID: projectID, Lane: "x", Tier: "T2", Status: registry.StatusReady, Branch: "conductor/T-1"}); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	merge := &fakeMerger{sha: "deadbeef"}
	cond, err := New(Deps{
		Store:       store,
		Picker:      registry.NewRegistry(store),
		Provisioner: &fakeProvisioner{},
		Engine:      &fakeEngine{verdict: engine.Verdict{Result: "pass"}},
		Verifier:    &fakeVerifier{result: "pass"},
		Merger:      merge,
		HostID:      "host-1",
		Emitter:     em,
		Differ:      differ,
		Policy:      policy,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return cond, store, merge
}

// scriptedSummary is the DiffSummary the fakeDiffer returns so the emitted
// KindDiff payload can be asserted field-by-field.
func scriptedSummary() events.DiffSummary {
	return events.DiffSummary{
		Branch: "conductor/T-1",
		Base:   "develop",
		Files: []events.DiffFile{
			{Path: "a.go", Status: "M", Additions: 3, Deletions: 1},
			{Path: "b.go", Status: "A", Additions: 10, Deletions: 0},
		},
		Patch:     "diff --git a/a.go b/a.go\n@@ -1 +1 @@\n-x\n+y\n",
		Truncated: false,
	}
}

// diffEvents returns the KindDiff events captured by the emitter.
func (r *recordingEmitter) diffEvents() []events.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []events.Event
	for _, e := range r.evts {
		if e.Kind == events.KindDiff {
			out = append(out, e)
		}
	}
	return out
}

// assertScriptedDiffPayload checks an emitted KindDiff event carries the scripted
// summary's fields, round-tripped through the wire map (branch/base/patch/
// truncated + per-file path/status/additions/deletions).
func assertScriptedDiffPayload(t *testing.T, ev events.Event) {
	t.Helper()
	if ev.Phase != events.PhaseReview {
		t.Fatalf("KindDiff phase = %q, want %q", ev.Phase, events.PhaseReview)
	}
	if ev.Project != projectID || ev.Task != "T-1" {
		t.Fatalf("KindDiff envelope missing project/task: %+v", ev)
	}
	if got := ev.Payload["branch"]; got != "conductor/T-1" {
		t.Fatalf("payload branch = %v, want conductor/T-1", got)
	}
	if got := ev.Payload["base"]; got != "develop" {
		t.Fatalf("payload base = %v, want develop", got)
	}
	if got := ev.Payload["patch"]; got != scriptedSummary().Patch {
		t.Fatalf("payload patch = %v, want scripted patch", got)
	}
	if got := ev.Payload["truncated"]; got != false {
		t.Fatalf("payload truncated = %v, want false", got)
	}
	files, ok := ev.Payload["files"].([]any)
	if !ok {
		t.Fatalf("payload files not a []any: %T", ev.Payload["files"])
	}
	if len(files) != 2 {
		t.Fatalf("payload files len = %d, want 2", len(files))
	}
	first, ok := files[0].(map[string]any)
	if !ok {
		t.Fatalf("payload files[0] not a map: %T", files[0])
	}
	if first["path"] != "a.go" || first["status"] != "M" {
		t.Fatalf("payload files[0] = %+v, want a.go/M", first)
	}
	// The event must validate (known phase/kind/project) so it survives the bus.
	if err := ev.Validate(); err != nil {
		t.Fatalf("KindDiff event invalid: %v", err)
	}
}

// TestConductor_Tick_AutoMerge_EmitsKindDiff proves a green-gate AUTO-MERGE tick
// emits exactly ONE KindDiff at PhaseReview carrying the scripted payload, IN
// ADDITION to the existing started/merge events, and still merges. The diff ran
// against the verified worktree.
func TestConductor_Tick_AutoMerge_EmitsKindDiff(t *testing.T) {
	ctx := context.Background()
	em := &recordingEmitter{}
	differ := &fakeDiffer{summary: scriptedSummary()}
	cond, store, merge := condWithDiffer(t, em, differ, nil)

	res, err := cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if res.Outcome != OutcomeMerged {
		t.Fatalf("outcome = %q, want merged", res.Outcome)
	}
	if merge.calls != 1 {
		t.Fatalf("merge calls = %d, want 1 (diff must not block the merge)", merge.calls)
	}
	if differ.calls != 1 {
		t.Fatalf("differ calls = %d, want exactly 1", differ.calls)
	}
	if differ.gotWS.Branch != "conductor/T-1" {
		t.Fatalf("differ ran against branch %q, want the verified conductor/T-1", differ.gotWS.Branch)
	}
	diffs := em.diffEvents()
	if len(diffs) != 1 {
		t.Fatalf("KindDiff events = %d, want exactly 1; kinds %v", len(diffs), em.kinds())
	}
	assertScriptedDiffPayload(t, diffs[0])
	// The existing lifecycle events are still present and unchanged.
	for _, want := range []events.Kind{events.KindStarted, events.KindMerge} {
		if !em.hasKind(want) {
			t.Errorf("expected a %q event alongside the diff, got kinds %v", want, em.kinds())
		}
	}
	if got := taskStatus(t, store, "T-1"); got != registry.StatusDone {
		t.Fatalf("task status = %q, want done", got)
	}
}

// TestConductor_Tick_Held_EmitsKindDiffBeforeHold proves a HELD task (high tier +
// default policy) ALSO emits a KindDiff at PhaseReview, and that the diff is
// emitted BEFORE the intervention-needed hold signal so a human reviewing the held
// task sees what changed. No merge happens.
func TestConductor_Tick_Held_EmitsKindDiffBeforeHold(t *testing.T) {
	ctx := context.Background()
	em := &recordingEmitter{}
	differ := &fakeDiffer{summary: scriptedSummary()}
	cond, store, merge := condWithDiffer(t, em, differ, governance.DefaultPolicy())
	// Make the task a high-risk tier the default policy holds for a human.
	tk, err := store.GetTask(ctx, "T-1")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	tk.Tier = governance.TierT4
	if err := store.UpdateTask(ctx, tk); err != nil {
		t.Fatalf("set tier: %v", err)
	}

	res, err := cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if res.Outcome != OutcomeHeld {
		t.Fatalf("outcome = %q, want held", res.Outcome)
	}
	if merge.calls != 0 {
		t.Fatalf("held task must NOT merge, got %d merge calls", merge.calls)
	}
	if differ.calls != 1 {
		t.Fatalf("differ calls = %d, want 1 (diff emitted before the hold)", differ.calls)
	}
	diffs := em.diffEvents()
	if len(diffs) != 1 {
		t.Fatalf("KindDiff events = %d, want exactly 1; kinds %v", len(diffs), em.kinds())
	}
	assertScriptedDiffPayload(t, diffs[0])
	// Ordering: the diff must precede the intervention-needed hold so a human sees
	// the change with the hold.
	diffIdx := firstKindIndex(em, events.KindDiff)
	holdIdx := firstKindIndex(em, events.KindInterventionNeeded)
	if diffIdx < 0 || holdIdx < 0 {
		t.Fatalf("expected both diff and intervention-needed events; kinds %v", em.kinds())
	}
	if diffIdx >= holdIdx {
		t.Fatalf("KindDiff (idx %d) must be emitted BEFORE intervention-needed (idx %d); kinds %v", diffIdx, holdIdx, em.kinds())
	}
	if got := taskStatus(t, store, "T-1"); got != StatusAwaitingApproval {
		t.Fatalf("held task status = %q, want %q", got, StatusAwaitingApproval)
	}
}

// TestConductor_Tick_DifferError_NoKindDiff_StillMerges proves a Differ that
// returns an ERROR does NOT fail the tick (still merges) and emits NO KindDiff:
// observability never breaks the loop (the error is swallowed + logged).
func TestConductor_Tick_DifferError_NoKindDiff_StillMerges(t *testing.T) {
	ctx := context.Background()
	em := &recordingEmitter{}
	differ := &fakeDiffer{err: errors.New("diff boom")}
	cond, _, merge := condWithDiffer(t, em, differ, nil)

	res, err := cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("Tick must not fail on a diff error: %v", err)
	}
	if res.Outcome != OutcomeMerged {
		t.Fatalf("outcome = %q, want merged (diff error must not alter the tick)", res.Outcome)
	}
	if merge.calls != 1 {
		t.Fatalf("merge calls = %d, want 1 despite the diff error", merge.calls)
	}
	if differ.calls != 1 {
		t.Fatalf("differ calls = %d, want 1", differ.calls)
	}
	if em.hasKind(events.KindDiff) {
		t.Fatalf("a diff error must emit NO KindDiff; kinds %v", em.kinds())
	}
}

// TestConductor_Tick_NilDiffer_NoKindDiff_BackwardCompat proves the default harness
// (no Differ) emits NO KindDiff. The green-path sequence is develop-started,
// verify-started, the Verifier verdict (a KindDecision, B1/ADR-0033), then merge —
// the verdict is the only addition over the pre-4C-1 lifecycle; crucially still NO diff.
func TestConductor_Tick_NilDiffer_NoKindDiff_BackwardCompat(t *testing.T) {
	ctx := context.Background()
	em := &recordingEmitter{}
	// condWithEmitter wires NO Differ, exactly the pre-4C-1 shape.
	cond, _ := condWithEmitter(t, em, engine.Verdict{Result: "pass"}, nil, "pass")

	res, err := cond.Tick(ctx, projectID)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if res.Outcome != OutcomeMerged {
		t.Fatalf("outcome = %q, want merged", res.Outcome)
	}
	if em.hasKind(events.KindDiff) {
		t.Fatalf("nil Differ must emit NO KindDiff; kinds %v", em.kinds())
	}
	// Green-path sequence: develop-started, verify-started, Verifier verdict
	// (decision, B1/ADR-0033), merge — no KindDiff (nil Differ).
	want := []events.Kind{events.KindStarted, events.KindStarted, events.KindDecision, events.KindMerge}
	got := em.kinds()
	if len(got) != len(want) {
		t.Fatalf("event sequence = %v, want byte-identical %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event[%d] = %q, want %q (sequence %v)", i, got[i], want[i], got)
		}
	}
}

// --- helpers shared by the diff tick tests ----------------------------------

func taskStatus(t *testing.T, store *statestore.MemoryStore, id string) string {
	t.Helper()
	tk, err := store.GetTask(context.Background(), id)
	if err != nil {
		t.Fatalf("get task %q: %v", id, err)
	}
	return tk.Status
}

func firstKindIndex(r *recordingEmitter, k events.Kind) int {
	for i, got := range r.kinds() {
		if got == k {
			return i
		}
	}
	return -1
}

// --- real GitDiffer git-mechanics unit test (hermetic, real git, no PG) ------

// TestGitDiffer_Diff_RealRepo proves NewGitDiffer().Diff computes the right
// bounded summary for a KNOWN change set: it inits a repo on develop, commits a
// base, cuts conductor/T-x with an add + a modify + a delete, checks it out in a
// real worktree, and asserts the per-file Files (path/status/additions/deletions),
// a non-empty Patch, and Truncated=false.
func TestGitDiffer_Diff_RealRepo(t *testing.T) {
	ctx := context.Background()
	clone := t.TempDir()
	gitT(t, clone, "init", "-q", "-b", "develop")
	gitT(t, clone, "config", "user.name", "test")
	gitT(t, clone, "config", "user.email", "test@test")

	// Base commit on develop: keep.txt (will be modified) and gone.txt (deleted).
	writeFileIn(t, clone, "keep.txt", "line1\nline2\nline3\n")
	writeFileIn(t, clone, "gone.txt", "remove me\n")
	gitT(t, clone, "add", ".")
	gitT(t, clone, "commit", "-q", "-m", "base")

	// Cut the task branch and make a KNOWN change set on it.
	branch := "conductor/proj-1/T-x"
	gitT(t, clone, "branch", branch)
	wt := filepath.Join(t.TempDir(), "wt")
	gitT(t, clone, "worktree", "add", "-q", wt, branch)
	// add new.txt (A), modify keep.txt (M), delete gone.txt (D).
	writeFileIn(t, wt, "new.txt", "alpha\nbeta\n")
	writeFileIn(t, wt, "keep.txt", "line1\nCHANGED\nline3\nline4\n")
	if err := os.Remove(filepath.Join(wt, "gone.txt")); err != nil {
		t.Fatalf("remove gone.txt: %v", err)
	}
	gitT(t, wt, "add", "-A")
	gitT(t, wt, "commit", "-q", "-m", "task changes")

	project := statestore.Project{ID: "proj-1", BaseBranch: "develop"}
	ws := engine.Workspace{Path: wt, Branch: branch}

	summary, err := NewGitDiffer().Diff(ctx, project, ws)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if summary.Branch != branch || summary.Base != "develop" {
		t.Fatalf("summary branch/base = %q/%q, want %q/develop", summary.Branch, summary.Base, branch)
	}
	if summary.Truncated {
		t.Fatalf("small diff must not be truncated")
	}
	if summary.Patch == "" {
		t.Fatalf("expected a non-empty patch")
	}
	byPath := map[string]events.DiffFile{}
	for _, f := range summary.Files {
		byPath[f.Path] = f
	}
	if len(byPath) != 3 {
		t.Fatalf("files = %+v, want exactly 3 (add/modify/delete)", summary.Files)
	}
	if f := byPath["new.txt"]; f.Status != "A" || f.Additions != 2 || f.Deletions != 0 {
		t.Fatalf("new.txt = %+v, want A +2 -0", f)
	}
	if f := byPath["keep.txt"]; f.Status != "M" || f.Additions != 2 || f.Deletions != 1 {
		t.Fatalf("keep.txt = %+v, want M +2 -1", f)
	}
	if f := byPath["gone.txt"]; f.Status != "D" || f.Additions != 0 || f.Deletions != 1 {
		t.Fatalf("gone.txt = %+v, want D +0 -1", f)
	}
}

// TestGitDiffer_Diff_LargeChange_Truncates proves the bounded caps hold for a LARGE
// change: with many files and a big patch, Truncated=true, len(Patch) <=
// maxPatchBytes, len(Files) <= maxFiles, AND the marshaled payload stays under the
// PG NOTIFY budget (< 8000 bytes).
func TestGitDiffer_Diff_LargeChange_Truncates(t *testing.T) {
	ctx := context.Background()
	clone := t.TempDir()
	gitT(t, clone, "init", "-q", "-b", "develop")
	gitT(t, clone, "config", "user.name", "test")
	gitT(t, clone, "config", "user.email", "test@test")
	writeFileIn(t, clone, "seed.txt", "seed\n")
	gitT(t, clone, "add", ".")
	gitT(t, clone, "commit", "-q", "-m", "base")

	branch := "conductor/proj-1/T-big"
	gitT(t, clone, "branch", branch)
	wt := filepath.Join(t.TempDir(), "wt")
	gitT(t, clone, "worktree", "add", "-q", wt, branch)

	// Many files, each with a sizable body, so both the file list and the patch blow
	// past the caps and the total-budget guard has to engage.
	body := ""
	for i := 0; i < 60; i++ {
		body += "this is a reasonably long content line to inflate the diff patch size\n"
	}
	for i := 0; i < 120; i++ {
		writeFileIn(t, wt, "file"+itoa(i)+".txt", body)
	}
	gitT(t, wt, "add", "-A")
	gitT(t, wt, "commit", "-q", "-m", "big change")

	project := statestore.Project{ID: "proj-1", BaseBranch: "develop"}
	ws := engine.Workspace{Path: wt, Branch: branch}

	summary, err := NewGitDiffer().Diff(ctx, project, ws)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !summary.Truncated {
		t.Fatalf("large diff must be truncated")
	}
	if len(summary.Patch) > defaultMaxPatchBytes {
		t.Fatalf("patch len = %d, want <= maxPatchBytes %d", len(summary.Patch), defaultMaxPatchBytes)
	}
	if len(summary.Files) > defaultMaxFiles {
		t.Fatalf("files len = %d, want <= maxFiles %d", len(summary.Files), defaultMaxFiles)
	}
	marshaled, err := json.Marshal(summary.Payload())
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if len(marshaled) >= 8000 {
		t.Fatalf("marshaled payload = %d bytes, want < 8000 (PG NOTIFY bound)", len(marshaled))
	}
}

// TestGitDiffer_Diff_EmptyDiff proves an identical branch yields empty Files, empty
// Patch, Truncated=false, and no error.
func TestGitDiffer_Diff_EmptyDiff(t *testing.T) {
	ctx := context.Background()
	clone := t.TempDir()
	gitT(t, clone, "init", "-q", "-b", "develop")
	gitT(t, clone, "config", "user.name", "test")
	gitT(t, clone, "config", "user.email", "test@test")
	writeFileIn(t, clone, "f.txt", "x\n")
	gitT(t, clone, "add", ".")
	gitT(t, clone, "commit", "-q", "-m", "base")

	branch := "conductor/proj-1/T-noop"
	gitT(t, clone, "branch", branch)
	wt := filepath.Join(t.TempDir(), "wt")
	gitT(t, clone, "worktree", "add", "-q", wt, branch) // no changes on the branch

	project := statestore.Project{ID: "proj-1", BaseBranch: "develop"}
	ws := engine.Workspace{Path: wt, Branch: branch}

	summary, err := NewGitDiffer().Diff(ctx, project, ws)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(summary.Files) != 0 || summary.Patch != "" || summary.Truncated {
		t.Fatalf("empty diff summary = %+v, want empty files/patch and not truncated", summary)
	}
}

// TestGitDiffer_Diff_MissingBase proves an unresolvable base returns a wrapped
// error (the conductor swallows it; the tick still merges).
func TestGitDiffer_Diff_MissingBase(t *testing.T) {
	ctx := context.Background()
	clone := t.TempDir()
	gitT(t, clone, "init", "-q", "-b", "develop")
	gitT(t, clone, "config", "user.name", "test")
	gitT(t, clone, "config", "user.email", "test@test")
	writeFileIn(t, clone, "f.txt", "x\n")
	gitT(t, clone, "add", ".")
	gitT(t, clone, "commit", "-q", "-m", "base")

	project := statestore.Project{ID: "proj-1", BaseBranch: "does-not-exist"}
	ws := engine.Workspace{Path: clone, Branch: "develop"}

	if _, err := NewGitDiffer().Diff(ctx, project, ws); err == nil {
		t.Fatalf("expected an error for an unresolvable base")
	}
}

// --- small local helpers ----------------------------------------------------

func writeFileIn(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// itoa is a tiny int->string for test file names without pulling strconv into the
// test's import churn (strconv is used by the production differ, not the tests).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

// TestGitDiffer_ReviewPatch_KeepsLateSourceWhereFullPatchTruncates reproduces the exact bug that
// false-blocked a correct task: the reviewer was fed FullPatch (--unified=1000000, whole-file
// context), so a LARGE generated file with a TINY change (its whole content embedded) blew past the
// byte cap and the cap TRUNCATED the later-sorting source file out — the reviewer then reported a
// present change as "missing from the diff". ReviewPatch (normal hunk diff) stays compact and keeps
// EVERY changed file visible. The big file (a-big.json) sorts before the source file (z-source.ts),
// mirroring apps/.../messages/*.json sorting before packages/shared/.../employee.ts.
func TestGitDiffer_ReviewPatch_KeepsLateSourceWhereFullPatchTruncates(t *testing.T) {
	ctx := context.Background()
	clone := t.TempDir()
	gitT(t, clone, "init", "-q", "-b", "develop")
	gitT(t, clone, "config", "user.name", "test")
	gitT(t, clone, "config", "user.email", "test@test")

	// a-big.json: LARGE (sorts first). z-source.ts: small source (sorts last).
	big := ""
	for i := 0; i < 200; i++ {
		big += "  \"key" + itoa(i) + "\": \"a fairly long translation string value to inflate the file\",\n"
	}
	writeFileIn(t, clone, "a-big.json", "{\n"+big+"}\n")
	writeFileIn(t, clone, "z-source.ts", "export const x = 1;\n")
	gitT(t, clone, "add", ".")
	gitT(t, clone, "commit", "-q", "-m", "base")

	branch := "conductor/proj-1/T-trunc"
	gitT(t, clone, "branch", branch)
	wt := filepath.Join(t.TempDir(), "wt")
	gitT(t, clone, "worktree", "add", "-q", wt, branch)
	// A TINY change to the big file + a small change to the source file.
	writeFileIn(t, wt, "a-big.json", "{\n"+big+"  \"added\": \"one new line\"\n}\n")
	writeFileIn(t, wt, "z-source.ts", "export const x = 2;\n")
	gitT(t, wt, "add", "-A")
	gitT(t, wt, "commit", "-q", "-m", "task changes")

	project := statestore.Project{ID: "proj-1", BaseBranch: "develop"}
	ws := engine.Workspace{Path: wt, Branch: branch}

	// A small cap so the big file's WHOLE-content FullPatch overflows it (truncating the
	// later-sorting source file out), while the compact ReviewPatch fits both files.
	d := NewGitDiffer(WithMaxFullPatchBytes(2000))

	full, fullTrunc, err := d.FullPatch(ctx, project, ws)
	if err != nil {
		t.Fatalf("FullPatch: %v", err)
	}
	if !fullTrunc {
		t.Fatalf("precondition: FullPatch should truncate the big whole-file diff at the 2000-byte cap")
	}
	if strings.Contains(full, "z-source.ts") {
		t.Fatalf("precondition: FullPatch should have cut the later-sorting z-source.ts out; it is present")
	}

	rev, revTrunc, err := d.ReviewPatch(ctx, project, ws)
	if err != nil {
		t.Fatalf("ReviewPatch: %v", err)
	}
	if revTrunc {
		t.Fatalf("ReviewPatch of a tiny change must NOT truncate")
	}
	if !strings.Contains(rev, "z-source.ts") || !strings.Contains(rev, "a-big.json") {
		t.Fatalf("ReviewPatch must keep EVERY changed file visible; got:\n%s", rev)
	}
}
