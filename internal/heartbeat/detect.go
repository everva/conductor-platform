package heartbeat

import (
	"fmt"
	"io"
)

// Detect is the independent stall-detector's pure core: it runs the Checker
// once, prints a one-line human-readable status to out, fires the Notifier on a
// non-fresh verdict, and returns the process exit code an external launchd/cron
// job should propagate — 0 when FRESH, non-zero otherwise. It is split out (and
// returns the code rather than calling os.Exit) so it is fully unit-testable.
//
// Exit-code contract (stable, for the external alert job):
//   - 0  FRESH    — daemon is alive and (if a progress window is set) advancing.
//   - 1  STALE    — heartbeat too old, or fresh-but-not-progressing (stuck).
//   - 2  MISSING  — no heartbeat file (daemon never started / is gone).
//   - 3  ERROR    — the heartbeat file is unreadable/corrupt (treated as "not alive").
func Detect(c *Checker, n Notifier, out io.Writer) int {
	res, err := c.Check()
	if err != nil {
		_, _ = fmt.Fprintf(out, "heartbeat: error: %v\n", err)
		return 3
	}

	_, _ = fmt.Fprintf(out, "heartbeat: %s — %s\n", res.Liveness, res.Reason)

	if res.Liveness.OK() {
		return 0
	}
	if n != nil {
		n.Notify(res)
	}
	if res.Liveness == Missing {
		return 2
	}
	return 1
}
