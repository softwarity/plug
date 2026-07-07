//go:build darwin

package tun

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	// The marker carries the cluster key, so the multicluster router can go the
	// OTHER way — PID → cluster — by reading it (clusterForPID). Harmless to the
	// single-cluster daemon, which only reads the marker's NAME (the pid).
	if os.WriteFile(marker, []byte(key), 0o644) != nil {
		return func() {}
	}
	return func() { _ = os.Remove(marker) }
}

// clusterForPID reports the cluster a registered launcher PID belongs to, by
// finding its marker across the per-cluster client dirs and reading the key it
// carries. It is the reverse of the registry (cluster→PIDs) and the lookup the
// multicluster router needs (PID→cluster; see walkToCluster in pidroute.go). A
// daemon holding N clusters caches this — the fs scan is the source of truth.
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

// ActiveClusters returns the keys of clusters that currently have at least one
// live client, read from the per-cluster client dirs (each marker carries its
// key). The global daemon reconciles its tunnel set against this — open a tunnel
// for each active cluster, close the rest — so the registry doubles as the IPC:
// a `plug -p X` just registers, and the daemon discovers cluster X. Stale markers
// are reaped on the way.
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
				_ = os.Remove(filepath.Join(dir, m.Name())) // reap stale
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
