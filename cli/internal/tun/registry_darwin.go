//go:build darwin

package tun

import (
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// The daemon shuts down once no `plug <cmd>` of its cluster is left running. Each
// client drops a PID marker here; the daemon counts the LIVE ones (a client
// killed with -9 leaves a stale marker, so liveness is checked, not just presence
// — this is the exact "kill and relaunch 10×/h" case).

// clientsDir is the per-cluster directory of live-client PID markers.
func clientsDir(key string) string { return filepath.Join(graftDir, ClusterHash(key)+".clients") }

// RegisterClient marks pid as a live client of the cluster and returns an
// unregister() that drops the marker (defer it). No-op on failure so a client
// never fails to launch just because the registry couldn't be written.
func RegisterClient(key string, pid int) func() {
	dir := clientsDir(key)
	if os.MkdirAll(dir, 0o755) != nil {
		return func() {}
	}
	marker := filepath.Join(dir, strconv.Itoa(pid))
	if os.WriteFile(marker, nil, 0o644) != nil {
		return func() {}
	}
	return func() { _ = os.Remove(marker) }
}

// LiveClients counts client markers whose process is still alive, reaping stale
// ones on the way (robust to a client killed with -9).
func LiveClients(key string) int {
	entries, err := os.ReadDir(clientsDir(key))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if processAlive(pid) {
			n++
		} else {
			_ = os.Remove(filepath.Join(clientsDir(key), e.Name()))
		}
	}
	return n
}

// processAlive reports whether pid is a live process, via a signal-0 probe:
// nil → alive; EPERM → alive (exists, not ours); ESRCH → dead.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
