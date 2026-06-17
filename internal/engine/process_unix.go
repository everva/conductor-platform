//go:build unix

package engine

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup starts the performer in its own process group so a
// control signal (and ctx-cancel kill) reaches the whole subprocess tree, not
// just the leader (ADR-0002, ADR-0011).
func configureProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}
