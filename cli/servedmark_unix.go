//go:build !windows

package main

import (
	"os"
	"syscall"
)

// ttyDevice is the terminal to ask a question on, whatever stdin was redirected to.
const ttyDevice = "/dev/tty"

// processAlive reports whether pid is a live process. Signal 0 performs the
// permission and existence checks without delivering anything — the standard
// probe. EPERM would mean "alive but not ours", which cannot happen for a
// record this user wrote.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid) // always succeeds on unix
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
