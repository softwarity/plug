package main

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup keeps the console's Ctrl-C away from cmd: a process started
// in a new process group does not get it.
func ownProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP
}
