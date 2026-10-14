//go:build darwin

package tun

import (
	"os"
	"syscall"
)

// lockRegistry holds the cluster's registry lock until the returned func runs
// (RegisterClientExclusive). Blocking: what it guards is a few file writes.
func lockRegistry(key string) func() {
	if os.MkdirAll(graftDir, 0o755) != nil {
		return func() {}
	}
	f, err := os.OpenFile(registryLockPath(key), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		return func() {}
	}
	if syscall.Flock(int(f.Fd()), syscall.LOCK_EX) != nil {
		f.Close()
		return func() {}
	}
	return func() { f.Close() } // closing the descriptor releases the flock
}

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
