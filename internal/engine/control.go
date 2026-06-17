package engine

// Control vocabulary (ADR-0011 §4): the typed set of control-channel verbs the
// platform may send a running engine via the FROZEN Command type. The frozen
// Command struct carries a free-form Action string; these constants + helpers
// formalize the legal values so callers (conductorctl's reverse channel, the
// conductor's pause-gate) share one vocabulary instead of scattering string
// literals. They name values on the existing type WITHOUT changing it.
const (
	// ActionPause tells the engine/loop to stop starting new work for a project
	// until resumed. A paused project's tick is a clean no-op (no lease, no
	// develop, no merge).
	ActionPause = "pause"
	// ActionResume clears a pause so ticks run again.
	ActionResume = "resume"
	// ActionAbort tells the engine to cancel in-flight work. It is part of the
	// frozen vocabulary; honoring it against a running performer is gated on the
	// tick's context cancellation (see ADR-0011 §4 follow-up).
	ActionAbort = "abort"
)

// PauseCommand returns the typed control directive that pauses a project's loop.
func PauseCommand() Command { return Command{Action: ActionPause} }

// ResumeCommand returns the typed control directive that resumes a project's loop.
func ResumeCommand() Command { return Command{Action: ActionResume} }

// AbortCommand returns the typed control directive that aborts in-flight work.
func AbortCommand() Command { return Command{Action: ActionAbort} }

// ValidAction reports whether action is a recognized control verb. It lets a
// reverse-channel consumer reject an unknown directive deterministically rather
// than acting on a typo.
func ValidAction(action string) bool {
	switch action {
	case ActionPause, ActionResume, ActionAbort:
		return true
	default:
		return false
	}
}
