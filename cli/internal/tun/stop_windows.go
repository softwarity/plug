//go:build windows

package tun

import "os"

// Terminate ends the child. Windows has no SIGTERM to relay to another
// process, and plug does not hold a console it could send Ctrl-Break to: the
// process is ended outright.
func Terminate(p *os.Process) { _ = p.Kill() }
