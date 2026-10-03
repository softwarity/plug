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

// markerOwnedByProcess has nothing to say on macOS, and says yes. The launcher
// is setuid root, so every marker under /var/run/plug is written with euid 0
// whoever ran it: the file's owner is root for everyone and tells the two
// accounts apart no better than no owner at all. The directory itself is
// root's and not writable by an ordinary account, which is the guard Windows
// lacks and this function exists for there; here the .uid sidecar and the
// start stamp are the identity.
func markerOwnedByProcess(string, int) bool { return true }
