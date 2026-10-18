//go:build !windows

package tun

import (
	"os"
	"syscall"
)

// Terminate ends the child as a SIGTERM aimed at plug does: the same signal,
// relayed, so a dev server shuts down the way it was written to.
func Terminate(p *os.Process) { _ = p.Signal(syscall.SIGTERM) }
