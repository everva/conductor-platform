package heartbeat

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"
)

// Liveness is the verdict the Checker reaches about a heartbeat.
type Liveness string

const (
	// Fresh means the heartbeat exists and is within the staleness threshold AND
	// (when a progress window is configured) has advanced its tick count recently.
	Fresh Liveness = "fresh"
	// Stale means the heartbeat exists but is older than the threshold, OR it keeps
	// being rewritten without the tick count advancing (no-progress / stuck).
	Stale Liveness = "stale"
	// Missing means there is no heartbeat file at all — the daemon never started,
	// was never configured to write one, or its file was removed.
	Missing Liveness = "missing"
)

// OK reports whether the liveness is healthy (Fresh). The independent detector
// maps !OK to a non-zero exit so an external launchd/cron job can alert.
func (l Liveness) OK() bool { return l == Fresh }

// Result is the outcome of a liveness check: the verdict plus the evidence
// behind it, so a caller (or the Notifier) can render a precise alert without
// re-reading the file.
type Result struct {
	// Liveness is the verdict.
	Liveness Liveness
	// Record is the heartbeat read, when present (zero value on Missing).
	Record Record
	// Age is how stale the heartbeat is relative to the Checker's clock (0 on
	// Missing). Negative ages (clock skew: heartbeat written "in the future") are
	// clamped to 0 so they are never treated as stale.
	Age time.Duration
	// Reason is a short human-readable explanation of the verdict.
	Reason string
}

// CheckerConfig tunes the staleness/progress decision.
type CheckerConfig struct {
	// Threshold is the maximum age a heartbeat may reach before it is Stale. It
	// must be positive; New rejects a non-positive threshold.
	Threshold time.Duration
	// Now is the injectable clock used to measure age. A nil Now uses time.Now
	// (UTC); tests inject a fixed clock for determinism.
	Now func() time.Time
}

// Checker reads a heartbeat file and decides liveness against an injected clock
// and threshold. It is the INDEPENDENT half of ADR-0016: it shares no state with
// the Writer beyond the file path, so it can judge a daemon that has died or
// hung. It is also stateful for progress detection — across repeated Check calls
// in one process it remembers the last tick it saw, so a heartbeat that is
// rewritten on time but never advances its tick is caught as Stale (stuck).
type Checker struct {
	path      string
	threshold time.Duration
	now       func() time.Time

	// progress tracking (in-process, across repeated Check calls).
	progressWindow time.Duration
	lastTick       uint64
	lastTickSeenAt time.Time
	haveSeen       bool
}

// New returns a Checker over path with the given config. A non-positive
// Threshold is a programming error and is rejected.
func New(path string, cfg CheckerConfig) (*Checker, error) {
	if path == "" {
		return nil, errors.New("heartbeat: checker path is required")
	}
	if cfg.Threshold <= 0 {
		return nil, fmt.Errorf("heartbeat: checker threshold must be positive (got %s)", cfg.Threshold)
	}
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Checker{path: path, threshold: cfg.Threshold, now: now}, nil
}

// WithProgressWindow enables progress-aware (no-progress) detection: if the
// heartbeat's tick count does not advance for longer than window — even while
// UpdatedAt stays fresh — the daemon is considered stuck and reported Stale. A
// non-positive window disables progress detection (staleness only). It returns
// the receiver for chaining at construction.
func (c *Checker) WithProgressWindow(window time.Duration) *Checker {
	c.progressWindow = window
	return c
}

// Check reads the heartbeat and returns its liveness Result. A missing file maps
// to Missing; a parse/validate error is a hard error (a corrupt heartbeat is not
// "alive"). It never calls os.Exit — the caller decides what to do with !OK.
func (c *Checker) Check() (Result, error) {
	now := c.now().UTC()

	rec, err := Read(c.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Result{Liveness: Missing, Reason: fmt.Sprintf("no heartbeat file at %q", c.path)}, nil
		}
		return Result{}, err
	}

	age := now.Sub(rec.UpdatedAt)
	if age < 0 {
		age = 0 // clock skew: never treat a future-stamped heartbeat as stale.
	}

	if age > c.threshold {
		return Result{
			Liveness: Stale,
			Record:   rec,
			Age:      age,
			Reason:   fmt.Sprintf("heartbeat age %s exceeds threshold %s", age.Round(time.Second), c.threshold),
		}, nil
	}

	// Progress-aware check: the heartbeat is fresh by mtime, but is it advancing?
	if c.progressWindow > 0 {
		if !c.haveSeen || rec.Tick != c.lastTick {
			// First sighting, or the tick advanced: progress observed, reset window.
			c.lastTick = rec.Tick
			c.lastTickSeenAt = now
			c.haveSeen = true
		} else if stuck := now.Sub(c.lastTickSeenAt); stuck > c.progressWindow {
			return Result{
				Liveness: Stale,
				Record:   rec,
				Age:      age,
				Reason: fmt.Sprintf("heartbeat fresh (age %s) but tick %d has not advanced for %s (> window %s): stuck",
					age.Round(time.Second), rec.Tick, stuck.Round(time.Second), c.progressWindow),
			}, nil
		}
	}

	return Result{
		Liveness: Fresh,
		Record:   rec,
		Age:      age,
		Reason:   fmt.Sprintf("heartbeat fresh (age %s within threshold %s)", age.Round(time.Second), c.threshold),
	}, nil
}

// Notifier is the alert seam (ADR-0016): it is invoked with a non-fresh Result so
// an out-of-process detector can raise an alert. It is deliberately a pure
// interface with no transport baked in — there are NO webhooks/email/credentials
// here. The default implementation logs structurally to stderr; the caller's
// exit code carries the machine-readable signal.
type Notifier interface {
	// Notify is called with a Result whose Liveness is not Fresh. Implementations
	// must not block indefinitely or panic.
	Notify(Result)
}

// LogNotifier is the default Notifier: it emits one structured slog line at WARN
// (Stale) or ERROR (Missing) describing the verdict. It is the only "transport"
// the product ships; real alerting (paging/webhook) is an out-of-tree concern an
// operator wires onto the process exit code or these log lines.
type LogNotifier struct {
	// Logger is the slog logger to emit on. A nil Logger falls back to a stderr
	// text handler so the default is always usable.
	Logger *slog.Logger
}

// Notify logs the stall verdict. Missing logs at ERROR (the daemon is gone),
// other non-fresh verdicts at WARN.
func (n LogNotifier) Notify(r Result) {
	logger := n.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	}
	attrs := []any{
		slog.String("liveness", string(r.Liveness)),
		slog.String("reason", r.Reason),
		slog.Duration("age", r.Age),
		slog.String("project", r.Record.Project),
		slog.String("host", r.Record.Host),
		slog.Int("pid", r.Record.PID),
		slog.Uint64("tick", r.Record.Tick),
		slog.String("last_outcome", r.Record.LastOutcome),
	}
	if r.Liveness == Missing {
		logger.Error("conductor heartbeat MISSING — daemon appears dead", attrs...)
		return
	}
	logger.Warn("conductor heartbeat STALE — daemon may be stalled", attrs...)
}

// NopNotifier is a Notifier that does nothing. Useful as an explicit "no alert"
// choice and in tests where the alert side-effect is irrelevant.
type NopNotifier struct{}

// Notify does nothing.
func (NopNotifier) Notify(Result) {}
