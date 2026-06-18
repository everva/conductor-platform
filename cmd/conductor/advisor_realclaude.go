//go:build realclaude

package main

import (
	"github.com/everva/conductor-platform/internal/sentinel"
)

// newAdvisor in the realclaude build wires the REAL `claude -p` gray-zone advisor
// (ADR-0006 Katman-2), guarded again at construction by CP_REAL_CLAUDE=1 inside
// sentinel.NewRealCommandAdvisor. It runs in the daemon's workspace root so the
// advisor inspects logs in context. This file is excluded from the default
// (offline) gate by the `realclaude` build tag, so the default daemon never links
// the real CLI runner.
func newAdvisor(cfg config) (sentinel.Advisor, error) {
	return sentinel.NewRealCommandAdvisor(cfg.rootDir)
}
