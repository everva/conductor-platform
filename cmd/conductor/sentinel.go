package main

import (
	"log/slog"

	"github.com/everva/conductor-platform/internal/conductor"
	"github.com/everva/conductor-platform/internal/sentinel"
)

// newSentinel builds the OPTIONAL 3-layer liveness progress-watchdog decider for
// the daemon (ADR-0006, Faz-2 2C-1). It returns a nil conductor.SentinelDecider
// (a true nil interface, so the conductor's nil-check works) unless -sentinel is
// set, keeping the default daemon behavior unchanged (Layer-1+3-only via the
// per-develop -timeout).
//
// When -sentinel is on it wires a *sentinel.Sentinel with:
//   - MaxTotal = sentinelMaxTotal (Layer-3 backstop; 0 falls back to the develop
//     -timeout so a process ceiling still bounds the run, but the watchdog's own
//     backstop is then disabled and the LLM is bounded only by the process timeout);
//   - GraceUnsure = sentinelGrace (gray-zone threshold; 0 = package default);
//   - an OPTIONAL gray-zone advisor (real `claude -p`) only when -sentinel-advisor
//     is set AND the build supports it (newAdvisor, behind the realclaude tag).
//     Otherwise the advisor is nil and the watchdog runs deterministic Layers 1+3.
//
// The Layer-3 backstop ALWAYS overrides the advisor inside Assess (it is checked
// first), so wiring the advisor never lets the LLM stall the run past the ceiling.
func newSentinel(cfg config, logger *slog.Logger) conductor.SentinelDecider {
	if !cfg.sentinel {
		return nil // true nil interface: conductor runs Layer-1+3-only behavior.
	}

	maxTotal := cfg.sentinelMaxTotal
	if maxTotal <= 0 {
		// No explicit backstop ceiling: fall back to the per-develop timeout so a
		// hung run is still bounded (by the process timeout) even with the watchdog's
		// own backstop disabled.
		maxTotal = cfg.timeout
	}

	var advisor sentinel.Advisor
	if cfg.sentinelAdvisor {
		adv, err := newAdvisor(cfg)
		if err != nil {
			// Advisor unavailable (e.g. CP_REAL_CLAUDE not set, or this build excludes
			// the real claude runner): degrade to deterministic Layers 1+3 rather than
			// failing the daemon. The backstop still bounds every run.
			logger.Warn("sentinel: gray-zone advisor requested but unavailable; running deterministic Layers 1+3 only",
				slog.String("error", err.Error()))
		} else {
			advisor = adv
			logger.Info("sentinel: gray-zone LLM advisor wired (real claude -p)")
		}
	}

	logger.Info("sentinel: 3-layer liveness watchdog ENABLED",
		slog.Duration("max_total", maxTotal),
		slog.Duration("grace", cfg.sentinelGrace),
		slog.Bool("advisor", advisor != nil))

	return sentinel.New(sentinel.Config{
		MaxTotal:    maxTotal,
		GraceUnsure: cfg.sentinelGrace,
	}, advisor)
}
