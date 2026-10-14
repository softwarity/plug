//go:build windows

package tun

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockRegistry holds the cluster's registry lock until the returned func runs
// (RegisterClientExclusive). Blocking: what it guards is a few file writes.
// The file may be another account's, made by the first launcher of this
// cluster: then it opens read-only, which is all LockFileEx needs.
func lockRegistry(key string) func() {
	if os.MkdirAll(graftDir, 0o755) != nil {
		return func() {}
	}
	path := registryLockPath(key)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o666)
	if err != nil {
		if f, err = os.Open(path); err != nil {
			return func() {}
		}
	}
	ol := new(windows.Overlapped)
	if windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, ol) != nil {
		f.Close()
		return func() {}
	}
	return func() {
		_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
		f.Close()
	}
}

// processAlive reports whether pid is a live process. Windows has no zombies,
// so OpenProcess succeeding + STILL_ACTIVE is enough. Liveness only — identity
// (a recycled PID) is the router walk's job (procStart), not this. The shared
// registry logic lives in registry.go.
func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if windows.GetExitCodeProcess(h, &code) != nil {
		return true // can't read the code → assume alive rather than reap a live client
	}
	const stillActive = 259 // STILL_ACTIVE
	return code == stillActive
}
