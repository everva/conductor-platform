package heartbeat

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeAt writes a heartbeat stamped at `at` to path, for checker tests.
func writeAt(t *testing.T, path string, tick uint64, at time.Time) {
	t.Helper()
	w := NewWriter(path, "h", "p", 100, fixedClock(at))
	if err := w.Write(tick, "noop"); err != nil {
		t.Fatalf("writeAt: %v", err)
	}
}

// TestCheckerLivenessStates covers FRESH (below threshold), STALE (above), and
// MISSING (no file) against an injected clock.
func TestCheckerLivenessStates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hb.json")
	written := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)
	threshold := time.Minute

	t.Run("fresh below threshold", func(t *testing.T) {
		writeAt(t, path, 1, written)
		c, err := New(path, CheckerConfig{Threshold: threshold, Now: fixedClock(written.Add(30 * time.Second))})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		res, err := c.Check()
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		if res.Liveness != Fresh || !res.Liveness.OK() {
			t.Fatalf("got %s, want fresh; reason=%q", res.Liveness, res.Reason)
		}
	})

	t.Run("stale above threshold", func(t *testing.T) {
		writeAt(t, path, 1, written)
		c, _ := New(path, CheckerConfig{Threshold: threshold, Now: fixedClock(written.Add(2 * time.Minute))})
		res, err := c.Check()
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		if res.Liveness != Stale || res.Liveness.OK() {
			t.Fatalf("got %s, want stale; reason=%q", res.Liveness, res.Reason)
		}
		if res.Age <= threshold {
			t.Fatalf("age %s should exceed threshold %s", res.Age, threshold)
		}
	})

	t.Run("missing when file absent", func(t *testing.T) {
		c, _ := New(filepath.Join(dir, "nope.json"), CheckerConfig{Threshold: threshold, Now: fixedClock(written)})
		res, err := c.Check()
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		if res.Liveness != Missing || res.Liveness.OK() {
			t.Fatalf("got %s, want missing", res.Liveness)
		}
	})

	t.Run("future-stamped heartbeat is fresh, not stale", func(t *testing.T) {
		writeAt(t, path, 1, written.Add(time.Hour)) // clock skew: future
		c, _ := New(path, CheckerConfig{Threshold: threshold, Now: fixedClock(written)})
		res, _ := c.Check()
		if res.Liveness != Fresh {
			t.Fatalf("future heartbeat got %s, want fresh (clamped age)", res.Liveness)
		}
		if res.Age != 0 {
			t.Fatalf("future heartbeat age = %s, want 0 (clamped)", res.Age)
		}
	})
}

// TestCheckerProgressAware proves no-progress detection: a heartbeat that stays
// fresh by mtime but whose tick never advances becomes STALE once the progress
// window elapses, and advancing the tick resets the window.
func TestCheckerProgressAware(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hb.json")
	t0 := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

	var now time.Time
	c, err := New(path, CheckerConfig{Threshold: time.Hour, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.WithProgressWindow(2 * time.Minute)

	// First sighting at tick 5: fresh, progress window starts.
	now = t0
	writeAt(t, path, 5, t0)
	if res, _ := c.Check(); res.Liveness != Fresh {
		t.Fatalf("first check got %s, want fresh", res.Liveness)
	}

	// 1 min later, still tick 5, heartbeat rewritten (fresh) but no progress: under
	// window -> still fresh.
	now = t0.Add(time.Minute)
	writeAt(t, path, 5, now)
	if res, _ := c.Check(); res.Liveness != Fresh {
		t.Fatalf("within-window no-progress check got %s, want fresh", res.Liveness)
	}

	// 3 min after first sighting, still tick 5: exceeds 2-min window -> STALE.
	now = t0.Add(3 * time.Minute)
	writeAt(t, path, 5, now)
	if res, _ := c.Check(); res.Liveness != Stale {
		t.Fatalf("beyond-window no-progress check got %s, want stale", res.Liveness)
	}

	// Tick advances to 6: progress observed -> fresh again, window reset.
	now = t0.Add(4 * time.Minute)
	writeAt(t, path, 6, now)
	if res, _ := c.Check(); res.Liveness != Fresh {
		t.Fatalf("after-progress check got %s, want fresh", res.Liveness)
	}
}

// TestNewRejectsBadConfig covers the construction guards.
func TestNewRejectsBadConfig(t *testing.T) {
	if _, err := New("", CheckerConfig{Threshold: time.Minute}); err == nil {
		t.Fatal("New should reject empty path")
	}
	if _, err := New("/x", CheckerConfig{Threshold: 0}); err == nil {
		t.Fatal("New should reject non-positive threshold")
	}
}

// recordingNotifier captures the last Result it was notified with (test seam).
type recordingNotifier struct {
	called bool
	last   Result
}

func (r *recordingNotifier) Notify(res Result) {
	r.called = true
	r.last = res
}

// TestNotifierSeamFiresOnStale proves Detect invokes the Notifier on STALE (and
// MISSING) but not on FRESH, and returns the contracted exit codes. It exercises
// the inner Detect function, never os.Exit.
func TestNotifierSeamFiresOnStale(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hb.json")
	written := time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC)

	t.Run("stale fires notifier, exit 1", func(t *testing.T) {
		writeAt(t, path, 1, written)
		c, _ := New(path, CheckerConfig{Threshold: time.Minute, Now: fixedClock(written.Add(5 * time.Minute))})
		n := &recordingNotifier{}
		code := Detect(c, n, &bytes.Buffer{})
		if code != 1 {
			t.Fatalf("Detect(stale) exit = %d, want 1", code)
		}
		if !n.called || n.last.Liveness != Stale {
			t.Fatalf("notifier not fired on stale: called=%v last=%s", n.called, n.last.Liveness)
		}
	})

	t.Run("missing fires notifier, exit 2", func(t *testing.T) {
		c, _ := New(filepath.Join(dir, "absent.json"), CheckerConfig{Threshold: time.Minute, Now: fixedClock(written)})
		n := &recordingNotifier{}
		code := Detect(c, n, &bytes.Buffer{})
		if code != 2 {
			t.Fatalf("Detect(missing) exit = %d, want 2", code)
		}
		if !n.called || n.last.Liveness != Missing {
			t.Fatalf("notifier not fired on missing")
		}
	})

	t.Run("fresh does not fire notifier, exit 0", func(t *testing.T) {
		writeAt(t, path, 1, written)
		c, _ := New(path, CheckerConfig{Threshold: time.Minute, Now: fixedClock(written.Add(10 * time.Second))})
		n := &recordingNotifier{}
		code := Detect(c, n, &bytes.Buffer{})
		if code != 0 {
			t.Fatalf("Detect(fresh) exit = %d, want 0", code)
		}
		if n.called {
			t.Fatal("notifier should NOT fire on fresh")
		}
	})

	t.Run("corrupt file is exit 3", func(t *testing.T) {
		bad := filepath.Join(dir, "bad.json")
		if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		c, _ := New(bad, CheckerConfig{Threshold: time.Minute, Now: fixedClock(written)})
		n := &recordingNotifier{}
		code := Detect(c, n, &bytes.Buffer{})
		if code != 3 {
			t.Fatalf("Detect(corrupt) exit = %d, want 3", code)
		}
	})
}

// TestNopNotifier and LogNotifier are exercised for coverage / no-panic.
func TestDefaultNotifiersDoNotPanic(t *testing.T) {
	NopNotifier{}.Notify(Result{Liveness: Stale})
	LogNotifier{}.Notify(Result{Liveness: Stale, Reason: "x"})
	LogNotifier{}.Notify(Result{Liveness: Missing, Reason: "y"})
}
