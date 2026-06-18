//go:build !realclaude

package main

import (
	"errors"

	"github.com/everva/conductor-platform/internal/sentinel"
)

// newAdvisor in the DEFAULT (offline) build never wires a real LLM advisor: the
// real `claude -p` runner lives only in the realclaude-tagged build, so the gate
// stays deterministic and offline. Requesting -sentinel-advisor here returns an
// error, which newSentinel logs and then degrades to deterministic Layers 1+3.
//
// The realclaude build (advisor_realclaude.go) overrides this with a constructor
// that wires sentinel.NewRealCommandAdvisor behind CP_REAL_CLAUDE=1.
func newAdvisor(_ config) (sentinel.Advisor, error) {
	return nil, errors.New("gray-zone advisor requires a realclaude build (build tag `realclaude` + CP_REAL_CLAUDE=1)")
}
