//go:build windows

package tun

import "golang.org/x/sys/windows"

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
