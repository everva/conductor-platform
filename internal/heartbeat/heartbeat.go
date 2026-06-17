// Package heartbeat is the PRODUCT-side liveness slice of ADR-0016: a heartbeat
// the conductor daemon writes each tick (and on shutdown), plus an INDEPENDENT
// detector that reads that heartbeat and decides whether the daemon is alive.
//
// The point of ADR-0016 is that a conductor cannot rescue its own death, so the
// reader half here is deliberately decoupled from the writer half: the Writer is
// called from inside the daemon's tick loop, while the Checker is meant to run
// OUT-OF-PROCESS (an external launchd/cron job invokes it). The two communicate
// only through a single small JSON file, so a dead/hung daemon leaves a stale or
// missing heartbeat that the independent Checker can still observe.
//
// It depends only on the standard library and an injectable clock (so liveness
// decisions are deterministic and testable), and it carries NO secret and no
// real alert transport: the Notifier is a seam with a logging default, never a
// webhook/email/credential (ADR-0016 constraint). This is the heartbeat +
// independent-stall-detector slice; Katman-2 (LLM-advisor) is deferred to Faz-1b.
package heartbeat

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// schemaVersion is the heartbeat record's schema version. It is written so a
// reader (possibly a different binary version, since the detector is
// independent) can detect an incompatible shape rather than silently
// misinterpreting it.
const schemaVersion = 1

// Record is the on-disk heartbeat the daemon writes each tick. Its JSON shape is
// stable and the single contract between the writing daemon and the independent
// reading detector, so the field tags here are load-bearing. It carries enough to
// judge both liveness (UpdatedAt) and progress (Tick / LastOutcome): a heartbeat
// whose UpdatedAt keeps advancing but whose Tick never moves is "stuck", which
// progress-aware checking can catch where plain staleness cannot (ADR-0016).
type Record struct {
	// SchemaVersion is the record schema version (currently 1).
	SchemaVersion int `json:"schema_version"`
	// Host is the host the daemon runs on (its lease host id / hostname).
	Host string `json:"host"`
	// PID is the daemon process id, so a reader can cross-check liveness with the
	// OS (kill -0 style) in addition to staleness.
	PID int `json:"pid"`
	// Project is the project id the daemon ticks.
	Project string `json:"project"`
	// Tick is the monotonically increasing count of ticks attempted. It is the
	// progress signal: if it does not advance across a window, the daemon is stuck
	// even if it keeps rewriting the heartbeat.
	Tick uint64 `json:"tick"`
	// LastOutcome is the outcome string of the most recent tick (e.g. "noop",
	// "merged", "retry") or "shutdown" on the final heartbeat. It is informational
	// progress context, not a liveness gate on its own.
	LastOutcome string `json:"last_outcome"`
	// UpdatedAt is when this heartbeat was written (UTC). It is the primary
	// liveness signal: staleness is measured from it.
	UpdatedAt time.Time `json:"updated_at"`
}

// Validate reports a malformed record (e.g. read from a truncated or wrong-schema
// file). It is intentionally lenient on optional fields and strict only on what
// liveness reasoning needs: a known schema and a non-zero timestamp.
func (r Record) Validate() error {
	if r.SchemaVersion != schemaVersion {
		return fmt.Errorf("heartbeat: unsupported schema_version %d (want %d)", r.SchemaVersion, schemaVersion)
	}
	if r.UpdatedAt.IsZero() {
		return fmt.Errorf("heartbeat: missing updated_at")
	}
	return nil
}

// Writer atomically writes heartbeat records to a fixed path. It is constructed
// once (with the daemon's stable identity) and Write is called each tick; the
// per-write fields (tick count, outcome, time) are passed in so the Writer holds
// no mutable counter of its own. A zero-value (nil) *Writer is a valid no-op, so
// a daemon with no heartbeat path configured needs no nil-guards at call sites.
type Writer struct {
	path    string
	host    string
	pid     int
	project string
	clock   func() time.Time
}

// NewWriter returns a Writer that writes to path with the given stable identity.
// clock is injectable for deterministic tests; a nil clock uses time.Now (UTC).
// An empty path returns a nil *Writer, which is a no-op Writer — that is how the
// daemon stays non-breaking when no heartbeat is configured.
func NewWriter(path, host, project string, pid int, clock func() time.Time) *Writer {
	if path == "" {
		return nil
	}
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &Writer{path: path, host: host, pid: pid, project: project, clock: clock}
}

// Write atomically persists a heartbeat for the given tick count and outcome. It
// writes a sibling temp file then renames it over the target, so a concurrent
// independent reader never observes a torn/partial record (rename is atomic on a
// POSIX filesystem). A nil *Writer (no path configured) is a no-op returning nil.
func (w *Writer) Write(tick uint64, outcome string) error {
	if w == nil {
		return nil
	}
	rec := Record{
		SchemaVersion: schemaVersion,
		Host:          w.host,
		PID:           w.pid,
		Project:       w.project,
		Tick:          tick,
		LastOutcome:   outcome,
		UpdatedAt:     w.clock().UTC(),
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("heartbeat: marshal: %w", err)
	}
	b = append(b, '\n')

	dir := filepath.Dir(w.path)
	tmp, err := os.CreateTemp(dir, ".heartbeat-*.tmp")
	if err != nil {
		return fmt.Errorf("heartbeat: create temp: %w", err)
	}
	tmpName := tmp.Name()
	// Best-effort cleanup if anything below fails before the rename succeeds.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("heartbeat: write temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("heartbeat: sync temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("heartbeat: close temp: %w", err)
	}
	if err := os.Rename(tmpName, w.path); err != nil {
		return fmt.Errorf("heartbeat: rename temp: %w", err)
	}
	return nil
}

// Read loads and validates the heartbeat record at path. A missing file is
// surfaced distinctly via os.IsNotExist so the Checker can map it to MISSING
// rather than treating it as a hard error.
func Read(path string) (Record, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Record{}, err
	}
	var rec Record
	if err := json.Unmarshal(b, &rec); err != nil {
		return Record{}, fmt.Errorf("heartbeat: parse %q: %w", path, err)
	}
	if err := rec.Validate(); err != nil {
		return Record{}, err
	}
	return rec, nil
}
