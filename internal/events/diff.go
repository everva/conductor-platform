package events

// This file is the typed, json-tagged payload contract for the KindDiff event
// (ADR-0030, finalized in 4C-1). The conductor computes a BOUNDED diff of a
// task's verified branch vs the project base when the INDEPENDENT verify gate
// passes and emits it as a KindDiff Event so the editor (4C-1) can render the
// change in a native diff view, reusing the existing event pipe
// (bus -> gateway -> bridge -> webview); no new endpoint is added.
//
// BOUNDEDNESS IS LOAD-BEARING. Events flow over Postgres LISTEN/NOTIFY, whose
// payload is capped at ~8KB (and WS frames should not bloat either), so the
// producer (conductor.GitDiffer) caps the file list and the unified patch and
// sets Truncated=true when it trims to fit that NOTIFY budget. This type only
// declares the wire shape; the caps live with the producer.
//
// This is ADDITIVE to the events package (ADR-0021): it touches neither the
// EventBus interface nor the Event envelope struct. The Event already carries
// Project+Task, so the payload deliberately does NOT duplicate them (that would
// be drift) — it carries only the diff-specific fields.

// DiffFile is one changed file in a DiffSummary: its path, the git name-status
// letter, and the added/deleted line counts. It is the per-file row a diff view
// lists (ADR-0030).
type DiffFile struct {
	// Path is the file path relative to the repo root. For a rename/copy git may
	// report a single combined path; the producer records what name-status emits.
	Path string `json:"path"`
	// Status is the git name-status letter for the change: A (added), M (modified),
	// D (deleted), R (renamed), C (copied), T (type-changed), etc.
	Status string `json:"status"`
	// Additions is the number of added lines, or 0 for a binary file (numstat "-").
	Additions int `json:"additions"`
	// Deletions is the number of deleted lines, or 0 for a binary file (numstat "-").
	Deletions int `json:"deletions"`
}

// DiffSummary is the BOUNDED KindDiff payload (ADR-0030): the changed-file list
// plus a capped unified patch of the task branch vs the base, with a Truncated
// flag set when the producer trimmed files and/or the patch to stay under the
// Postgres LISTEN/NOTIFY ~8KB bound. It is emitted at PhaseReview when the
// independent verify gate passes (covering both auto-merge and held-for-approval
// tasks), so a human reviewing a held task can see what changed.
type DiffSummary struct {
	// Branch is the task's verified per-task branch the diff is computed for.
	Branch string `json:"branch"`
	// Base is the project base branch the diff is computed against.
	Base string `json:"base"`
	// Files is the per-file change list, capped by the producer to fit the NOTIFY
	// budget (Truncated reports when it was capped).
	Files []DiffFile `json:"files"`
	// Patch is the unified diff, capped to a byte budget by the producer (cut on a
	// line boundary). Truncated reports when it was capped.
	Patch string `json:"patch"`
	// Truncated is true when the producer capped the files and/or the patch to fit
	// the Postgres LISTEN/NOTIFY ~8KB budget — a UI surfaces "diff truncated".
	Truncated bool `json:"truncated"`
}

// Payload renders the summary as the wire map an events.Event carries in its
// Payload field. It builds the map directly with keys matching the json tags
// above (kept in lockstep by the round-trip tests), which preserves int line
// counts for Go-side consumers rather than the float64 a JSON unmarshal would
// yield; once the Event is serialized to the PG json column / WS frame the
// on-wire shape is exactly those json tags either way. The Files slice is a
// []any of map[string]any — the same structure a JSON round-trip through Postgres
// produces — so a consumer reading Event.Payload sees one shape on both the
// in-memory bus and the PG path.
func (s DiffSummary) Payload() map[string]any {
	return map[string]any{
		"branch":    s.Branch,
		"base":      s.Base,
		"files":     filesPayload(s.Files),
		"patch":     s.Patch,
		"truncated": s.Truncated,
	}
}

// filesPayload converts the typed file list into the []any of map[string]any a
// generic event payload carries (the shape a JSON round-trip through PG yields),
// so consumers reading Event.Payload see the same structure whether the event
// came straight from the conductor or back out of Postgres. A nil/empty slice
// yields an empty (non-nil) slice so the wire shape is a stable [] not null.
func filesPayload(files []DiffFile) []any {
	out := make([]any, 0, len(files))
	for _, f := range files {
		out = append(out, map[string]any{
			"path":      f.Path,
			"status":    f.Status,
			"additions": f.Additions,
			"deletions": f.Deletions,
		})
	}
	return out
}
