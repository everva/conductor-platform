package conductor

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/everva/conductor-platform/internal/engine"
	"github.com/everva/conductor-platform/internal/events"
	"github.com/everva/conductor-platform/internal/statestore"
)

// GitDiffer is the real Differ (ADR-0030, 4C-1): it computes a BOUNDED diff of a
// task's verified branch vs the project base by shelling out to git in the task
// worktree (reusing the package's gitOut helper, the same mechanic GitMerger
// uses) and returns it as an events.DiffSummary. The conductor emits that as a
// KindDiff event at the green gate so the editor can render the change.
//
// BOUNDEDNESS IS THE WHOLE POINT. Events travel over Postgres LISTEN/NOTIFY,
// whose payload is capped at ~8KB. GitDiffer therefore caps three things: the
// per-file list (maxFiles), the unified patch (maxPatchBytes, cut on a line
// boundary), AND — as a final guarantee independent of long path names — the
// TOTAL marshaled payload (maxPayloadBytes), trimming the patch then dropping
// trailing files until the marshaled Payload() fits. Any cap that fires sets
// Truncated=true. Defaults are chosen so the total stays comfortably under the
// NOTIFY limit.
//
// It is read-only: it runs only `git diff` variants and never mutates the
// worktree, so it can be called from the tick without disturbing the merge.
type GitDiffer struct {
	// maxPatchBytes caps the unified patch length (cut on a line boundary). Default
	// defaultMaxPatchBytes.
	maxPatchBytes int
	// maxFiles caps the per-file change list. Default defaultMaxFiles.
	maxFiles int
	// maxPayloadBytes is the final TOTAL-budget guard on the marshaled Payload();
	// after building the summary, the patch then trailing files are trimmed until
	// json.Marshal(Payload()) fits this. Default defaultMaxPayloadBytes, chosen
	// below the PG NOTIFY ~8KB limit with headroom for the Event envelope.
	maxPayloadBytes int
	// maxFullPatchBytes caps the FULL-context patch (P2b) FullPatch produces; it is
	// persisted out-of-band, so it is far larger than maxPatchBytes. Default
	// defaultMaxFullPatchBytes.
	maxFullPatchBytes int
}

// Diff caps (ADR-0030): defaults chosen so the TOTAL marshaled DiffSummary
// payload stays safely under the Postgres LISTEN/NOTIFY ~8KB limit even with the
// surrounding Event envelope.
const (
	// defaultMaxPatchBytes caps the unified patch (cut on a line boundary).
	defaultMaxPatchBytes = 4096
	// defaultMaxFiles caps the per-file change list.
	defaultMaxFiles = 40
	// defaultMaxPayloadBytes is the final total-budget guard on the marshaled
	// payload — the hard guarantee the NOTIFY bound holds regardless of path names.
	defaultMaxPayloadBytes = 7000
	// defaultMaxFullPatchBytes caps the FULL-context patch (P2b, ADR-0041) that is
	// persisted OUT-OF-BAND (not over NOTIFY) for the editor's full-file native diff. It
	// is far larger than the NOTIFY-bounded caps above (whole-file context is the point)
	// but still bounded so a pathological diff can't store an unbounded blob; over it, the
	// patch is cut on a line boundary and Truncated is set (the editor still renders what fit).
	defaultMaxFullPatchBytes = 1 << 20 // 1 MiB
)

// DiffOption configures a GitDiffer at construction (additive; ADR-0021). Options
// are applied in order after the defaults are set.
type DiffOption func(*GitDiffer)

// WithMaxPatchBytes overrides the unified-patch byte cap. A non-positive value is
// ignored (keeps the default), so a misconfigured option cannot disable the cap.
func WithMaxPatchBytes(n int) DiffOption {
	return func(d *GitDiffer) {
		if n > 0 {
			d.maxPatchBytes = n
		}
	}
}

// WithMaxFiles overrides the changed-file list cap. A non-positive value is
// ignored (keeps the default).
func WithMaxFiles(n int) DiffOption {
	return func(d *GitDiffer) {
		if n > 0 {
			d.maxFiles = n
		}
	}
}

// WithMaxPayloadBytes overrides the total marshaled-payload budget guard. A
// non-positive value is ignored (keeps the default), so the NOTIFY-bound
// guarantee cannot be turned off.
func WithMaxPayloadBytes(n int) DiffOption {
	return func(d *GitDiffer) {
		if n > 0 {
			d.maxPayloadBytes = n
		}
	}
}

// WithMaxFullPatchBytes overrides the FULL-context patch byte cap (P2b). A non-positive
// value is ignored (keeps the default).
func WithMaxFullPatchBytes(n int) DiffOption {
	return func(d *GitDiffer) {
		if n > 0 {
			d.maxFullPatchBytes = n
		}
	}
}

// NewGitDiffer returns a GitDiffer with the bounded-diff caps defaulted to fit
// the PG NOTIFY budget (ADR-0030). Options override individual caps; with none it
// is the production-safe default.
func NewGitDiffer(opts ...DiffOption) *GitDiffer {
	d := &GitDiffer{
		maxPatchBytes:     defaultMaxPatchBytes,
		maxFiles:          defaultMaxFiles,
		maxPayloadBytes:   defaultMaxPayloadBytes,
		maxFullPatchBytes: defaultMaxFullPatchBytes,
	}
	for _, opt := range opts {
		opt(d)
	}
	return d
}

// Compile-time assertion that *GitDiffer satisfies the Differ seam.
var _ Differ = (*GitDiffer)(nil)

// Diff computes the bounded branch-vs-base diff for the task worktree (ADR-0030).
// base = project.BaseBranch, branch = ws.Branch; all git runs in ws.Path. It uses
// the THREE-dot range (<base>...HEAD = merge-base..branch) so it is robust to the
// base advancing after the worktree was cut. An empty diff yields empty Files,
// empty Patch, Truncated=false and no error. If <base> does not resolve it
// returns a wrapped error — the caller (conductor.emitDiff) swallows it so the
// tick still merges (observability never breaks the loop).
func (d *GitDiffer) Diff(ctx context.Context, project statestore.Project, ws engine.Workspace) (events.DiffSummary, error) {
	base := project.BaseBranch
	branch := ws.Branch
	if base == "" {
		return events.DiffSummary{}, fmt.Errorf("git differ: project %q has no base branch", project.ID)
	}
	// The three-dot range against HEAD diffs the merge-base..branch, robust to base
	// drift. A single combined spec keeps numstat/name-status/patch consistent.
	rng := base + "...HEAD"

	// Resolve the base up front so a missing base surfaces as a clear wrapped error
	// (the caller swallows it) rather than three confusing per-command failures.
	if _, err := gitOut(ctx, ws.Path, "rev-parse", "--verify", "--quiet", base); err != nil {
		return events.DiffSummary{}, fmt.Errorf("git differ: resolve base %q in %q: %w", base, ws.Path, err)
	}

	files, filesTrunc, err := d.changedFiles(ctx, ws.Path, rng)
	if err != nil {
		return events.DiffSummary{}, err
	}

	patchOut, err := gitOut(ctx, ws.Path, "diff", rng)
	if err != nil {
		return events.DiffSummary{}, fmt.Errorf("git differ: patch %q: %w", rng, err)
	}
	patch, patchTrunc := capPatch(patchOut, d.maxPatchBytes)

	summary := events.DiffSummary{
		Branch:    branch,
		Base:      base,
		Files:     files,
		Patch:     patch,
		Truncated: filesTrunc || patchTrunc,
	}

	// Final TOTAL-budget guard: trim the patch, then drop trailing files, until the
	// marshaled Payload() fits maxPayloadBytes. This GUARANTEES the NOTIFY bound
	// holds regardless of long path names that name-status/numstat may have emitted.
	d.fitBudget(&summary)
	return summary, nil
}

// Compile-time assertion that *GitDiffer also satisfies the OPTIONAL P2b FullDiffer seam
// (full-file native diff source), in addition to the bounded Differ.
var _ FullDiffer = (*GitDiffer)(nil)

// FullPatch computes the FULL-context unified patch of the task branch vs the project base
// (P2b, ADR-0041) for OUT-OF-BAND persistence — the editor fetches it to render a full-file
// NATIVE diff, beyond the bounded KindDiff event. Unlike Diff (capped for the NOTIFY bus),
// it runs `git diff --unified=<huge>` so each changed file's ENTIRE content rides along as
// context, letting the editor reconstruct whole-file before/after. It uses the SAME three-dot
// range (<base>...HEAD) and base-resolution as Diff, and caps the result at maxFullPatchBytes
// (cut on a line boundary), reporting truncation. Read-only; never mutates the worktree.
func (d *GitDiffer) FullPatch(ctx context.Context, project statestore.Project, ws engine.Workspace) (string, bool, error) {
	base := project.BaseBranch
	if base == "" {
		return "", false, fmt.Errorf("git differ: project %q has no base branch", project.ID)
	}
	if _, err := gitOut(ctx, ws.Path, "rev-parse", "--verify", "--quiet", base); err != nil {
		return "", false, fmt.Errorf("git differ: resolve base %q in %q: %w", base, ws.Path, err)
	}
	// --unified with a very large context count includes each changed file's whole content as
	// context (git clamps to the file length), so reconstruction yields the full file, not just
	// hunks. Same three-dot range as Diff (robust to base drift).
	out, err := gitOut(ctx, ws.Path, "diff", "--unified=1000000", base+"...HEAD")
	if err != nil {
		return "", false, fmt.Errorf("git differ: full patch %q...HEAD: %w", base, err)
	}
	patch, truncated := capPatch(out, d.maxFullPatchBytes)
	return patch, truncated, nil
}

// changedFiles builds the per-file change list for the range by merging numstat
// (additions/deletions + path; binary files show "-"/"-" -> 0/0) with name-status
// (the status letter), keyed by path. It caps the list to maxFiles, reporting
// truncation. The numstat pass is authoritative for the file set/order; a
// name-status entry only contributes its status letter.
func (d *GitDiffer) changedFiles(ctx context.Context, dir, rng string) ([]events.DiffFile, bool, error) {
	numstat, err := gitOut(ctx, dir, "diff", "--numstat", rng)
	if err != nil {
		return nil, false, fmt.Errorf("git differ: numstat %q: %w", rng, err)
	}
	nameStatus, err := gitOut(ctx, dir, "diff", "--name-status", rng)
	if err != nil {
		return nil, false, fmt.Errorf("git differ: name-status %q: %w", rng, err)
	}

	status := parseNameStatus(nameStatus)

	var files []events.DiffFile
	truncated := false
	for _, line := range strings.Split(numstat, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		// numstat columns are TAB-separated: "<add>\t<del>\t<path>". A binary file
		// shows "-\t-\t<path>". A rename shows the path as "old => new" or with NUL in
		// -z mode; we are not in -z mode, so take everything after the second tab.
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		path := parts[2]
		if len(files) >= d.maxFiles {
			truncated = true
			break
		}
		files = append(files, events.DiffFile{
			Path:      path,
			Status:    status[path], // "" when name-status had no row for it (defensive)
			Additions: parseCount(parts[0]),
			Deletions: parseCount(parts[1]),
		})
	}
	return files, truncated, nil
}

// parseNameStatus maps each changed path to its git name-status letter. Each line
// is "<status>\t<path>" (renames/copies append extra tab-separated paths after a
// similarity-scored status like R100; we key off the FINAL path, the new name,
// which is what numstat reports). A line without a tab is skipped.
func parseNameStatus(out string) map[string]string {
	m := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		cols := strings.Split(line, "\t")
		if len(cols) < 2 {
			continue
		}
		// The status letter is the first rune of cols[0] (e.g. "R100" -> "R"). The
		// path numstat reports for a rename is the destination = the LAST column.
		letter := string([]rune(strings.TrimSpace(cols[0]))[0:1])
		path := cols[len(cols)-1]
		m[path] = letter
	}
	return m
}

// parseCount parses a numstat add/del count; git emits "-" for a binary file,
// which (per ADR-0030) becomes 0. A malformed value also yields 0 rather than
// failing the whole diff (observability is best-effort).
func parseCount(s string) int {
	s = strings.TrimSpace(s)
	if s == "-" || s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// capPatch truncates patch to at most max bytes, cutting on a LINE boundary so a
// consumer never sees a half-line. It returns the (possibly trimmed) patch and
// whether it was truncated. A patch already within budget is returned unchanged.
func capPatch(patch string, max int) (string, bool) {
	if len(patch) <= max {
		return patch, false
	}
	cut := patch[:max]
	// Prefer cutting at the last newline within the budget so the output ends on a
	// whole line. If there is no newline at all in the window, fall back to the hard
	// byte cut (a single pathological long line).
	if i := strings.LastIndexByte(cut, '\n'); i >= 0 {
		cut = cut[:i+1]
	}
	return cut, true
}

// fitBudget enforces the final TOTAL-budget guarantee (ADR-0030): while the
// marshaled Payload() exceeds maxPayloadBytes, it first trims the Patch (halving
// toward empty, on line boundaries), then, once the patch is gone, drops trailing
// Files one at a time. Any trim sets Truncated=true. This makes the NOTIFY bound
// hold regardless of long path names that the file list may carry. It is a no-op
// when the payload already fits.
func (d *GitDiffer) fitBudget(s *events.DiffSummary) {
	if d.payloadFits(*s) {
		return
	}
	// 1) Shrink the patch first (the usually-dominant field), on line boundaries,
	// until it either fits or is empty. Halving converges fast without re-marshaling
	// per byte.
	for len(s.Patch) > 0 && !d.payloadFits(*s) {
		s.Truncated = true
		next := len(s.Patch) / 2
		trimmed, _ := capPatch(s.Patch, next)
		// capPatch cuts on a line boundary; if that left the length unchanged (a
		// single huge line), force progress with a hard byte cut.
		if len(trimmed) >= len(s.Patch) {
			trimmed = s.Patch[:next]
		}
		s.Patch = trimmed
	}
	if d.payloadFits(*s) {
		return
	}
	// 2) Patch is exhausted but path names alone still overflow: drop trailing files
	// until it fits (or none remain — an empty payload always fits).
	for len(s.Files) > 0 && !d.payloadFits(*s) {
		s.Truncated = true
		s.Files = s.Files[:len(s.Files)-1]
	}
}

// payloadFits reports whether the summary's marshaled Payload() is within the
// total-budget guard. A marshal error is treated as "does not fit" so the guard
// keeps trimming rather than emitting an unbounded payload.
func (d *GitDiffer) payloadFits(s events.DiffSummary) bool {
	b, err := json.Marshal(s.Payload())
	if err != nil {
		return false
	}
	return len(b) <= d.maxPayloadBytes
}
