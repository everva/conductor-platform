package heartbeat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixedClock returns a clock function pinned to t, for deterministic write/read.
func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

// TestWriteReadRoundTrip proves a written heartbeat reads back identically.
func TestWriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hb.json")
	at := time.Date(2026, 6, 18, 10, 0, 0, 0, time.UTC)

	w := NewWriter(path, "host-1", "proj-1", 4242, fixedClock(at))
	if w == nil {
		t.Fatal("NewWriter returned nil for a non-empty path")
	}
	if err := w.Write(7, "merged"); err != nil {
		t.Fatalf("Write: %v", err)
	}

	rec, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if rec.Host != "host-1" || rec.Project != "proj-1" || rec.PID != 4242 {
		t.Fatalf("identity mismatch: %+v", rec)
	}
	if rec.Tick != 7 || rec.LastOutcome != "merged" {
		t.Fatalf("progress mismatch: tick=%d outcome=%q", rec.Tick, rec.LastOutcome)
	}
	if !rec.UpdatedAt.Equal(at) {
		t.Fatalf("UpdatedAt = %s, want %s", rec.UpdatedAt, at)
	}
	if rec.SchemaVersion != schemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", rec.SchemaVersion, schemaVersion)
	}
}

// TestWriteAtomicNoTornFile asserts the temp-then-rename strategy never leaves a
// torn/partial file at the target: after a successful Write the only file at the
// path parses cleanly, and no leftover temp files remain in the directory.
func TestWriteAtomicNoTornFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hb.json")
	w := NewWriter(path, "h", "p", 1, fixedClock(time.Now().UTC()))

	// Overwrite many times; each read in between must be a complete, valid record.
	for i := uint64(1); i <= 50; i++ {
		if err := w.Write(i, "noop"); err != nil {
			t.Fatalf("Write #%d: %v", i, err)
		}
		rec, err := Read(path)
		if err != nil {
			t.Fatalf("Read after Write #%d: %v (torn?)", i, err)
		}
		if rec.Tick != i {
			t.Fatalf("Read after Write #%d: tick=%d", i, rec.Tick)
		}
	}

	// No leftover temp files: only the target remains.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("leftover temp file: %s", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 file in dir, got %d", len(entries))
	}
}

// TestNilWriterIsNoop proves an empty path yields a nil Writer whose Write is a
// safe no-op — this is what keeps the daemon non-breaking when -heartbeat is
// unset.
func TestNilWriterIsNoop(t *testing.T) {
	w := NewWriter("", "h", "p", 1, nil)
	if w != nil {
		t.Fatal("NewWriter(\"\") should return nil")
	}
	if err := w.Write(1, "noop"); err != nil {
		t.Fatalf("nil Writer.Write should be a no-op, got %v", err)
	}
}

// TestReadMissingFile maps an absent file to a not-exist error (the Checker turns
// this into MISSING).
func TestReadMissingFile(t *testing.T) {
	_, err := Read(filepath.Join(t.TempDir(), "absent.json"))
	if !os.IsNotExist(err) {
		t.Fatalf("Read(absent) err = %v, want IsNotExist", err)
	}
}

// TestReadRejectsBadSchema proves a wrong schema_version is rejected rather than
// silently misinterpreted.
func TestReadRejectsBadSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hb.json")
	bad, _ := json.Marshal(Record{SchemaVersion: 999, UpdatedAt: time.Now()})
	if err := os.WriteFile(path, bad, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := Read(path); err == nil {
		t.Fatal("Read should reject an unsupported schema_version")
	}
}
