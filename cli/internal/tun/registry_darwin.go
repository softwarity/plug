//go:build darwin

package tun

import "syscall"

// processAlive reports whether pid is a live process, via a signal-0 probe:
// nil → alive; EPERM → alive (exists, not ours); ESRCH → dead. The shared
// registry logic lives in registry.go.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
