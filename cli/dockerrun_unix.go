//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup keeps the terminal's signals away from cmd: the Ctrl-C the
// kernel delivers to the foreground group, and the SIGHUP of a closed
// terminal. Added to what dockerCommand set (the dropped credentials), not in
// place of it.
func ownProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}
