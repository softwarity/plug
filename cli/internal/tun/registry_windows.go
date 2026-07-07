//go:build windows

package tun

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/windows"
)

// Windows client registry — the mirror of registry_darwin.go, and the "IPC" between
// the non-elevated launcher and the elevated SYSTEM service. Each `plug <cmd>` drops
// a marker (its PID, carrying its cluster key) under a machine-wide ProgramData dir
// (graftDir, see graft_windows.go); the service's reconcile loop reads them to open
// one tunnel per active cluster. A file registry (not a named pipe) keeps this
// identical to macOS and lets a user register WITHOUT admin — that, plus the service
// holding the WinTUN, is what removes the per-run elevation.
//
// NOTE(win): intentionally duplicates the darwin logic for now so the validated mac
// path is untouched; once the Windows datapath is validated we factor the shared
// bits into one darwin||windows file. See docs/windows-service.md.

func clientsDir(key string) string { return filepath.Join(graftDir, ClusterHash(key)+".clients") }

// RegisterClient marks pid as a live client of the cluster (the marker carries the
// key, so the service can map PID→cluster) and returns an unregister() to drop it.
func RegisterClient(key string, pid int) func() {
	dir := clientsDir(key)
	if os.MkdirAll(dir, 0o755) != nil {
		return func() {}
	}
	marker := filepath.Join(dir, strconv.Itoa(pid))
	if os.WriteFile(marker, []byte(key), 0o644) != nil {
		return func() {}
	}
	return func() { _ = os.Remove(marker) }
}

// clusterForPID reports the cluster a registered launcher PID belongs to (PID→cluster,
// the reverse the router walk needs) by finding its marker and reading the key.
func clusterForPID(pid int) (string, bool) {
	entries, err := os.ReadDir(graftDir)
	if err != nil {
		return "", false
	}
	name := strconv.Itoa(pid)
	for _, e := range entries {
		if !e.IsDir() || !strings.HasSuffix(e.Name(), ".clients") {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(graftDir, e.Name(), name)); err == nil {
			return strings.TrimSpace(string(b)), true
		}
	}
	return "", false
}

// LiveClients counts client markers whose process is still alive, reaping stale ones.
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

// ActiveClusters returns the keys of clusters with at least one live client — the
// service reconciles its tunnel set against this. Stale markers are reaped on the way.
func ActiveClusters() []string {
	entries, err := os.ReadDir(graftDir)
	if err != nil {
		return nil
	}
	var keys []string
	for _, e := range entries {
		if !e.IsDir() || !strings.HasSuffix(e.Name(), ".clients") {
			continue
		}
		dir := filepath.Join(graftDir, e.Name())
		markers, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		key := ""
		for _, m := range markers {
			pid, err := strconv.Atoi(m.Name())
			if err != nil {
				continue
			}
			if !processAlive(pid) {
				_ = os.Remove(filepath.Join(dir, m.Name()))
				continue
			}
			if key == "" {
				if b, err := os.ReadFile(filepath.Join(dir, m.Name())); err == nil {
					key = strings.TrimSpace(string(b))
				}
			}
		}
		if key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

// processAlive reports whether pid is a live process. Windows has no zombies, so
// OpenProcess succeeding + STILL_ACTIVE is enough. Liveness only — identity (a
// recycled PID) is the router walk's job (procStart), not this.
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
