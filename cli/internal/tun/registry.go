//go:build darwin || windows

package tun

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// The client registry, shared by the macOS daemon and the Windows service (the
// per-OS bit is only processAlive, in registry_<os>.go). Each `plug <cmd>`
// drops a PID marker carrying its cluster key under graftDir; the global
// datapath owner (daemon / SYSTEM service) counts the LIVE ones to know which
// clusters are active and when to shut down. A file registry (not a pipe)
// lets a client register WITHOUT elevation, and liveness is checked — not just
// presence — so a client killed with -9 never wedges the count (the exact
// "kill and relaunch 10×/h" case). Linux needs none of this: each launch owns
// its private datapath via its mount namespace.

// ClusterHash is the short, filesystem-safe id for a cluster key (host:port).
func ClusterHash(key string) string {
	sum := sha1.Sum([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// clientsDir is the per-cluster directory of live-client PID markers.
func clientsDir(key string) string { return filepath.Join(graftDir, ClusterHash(key)+".clients") }

// RegisterClient marks pid as a live client of the cluster and returns an
// unregister() that drops the marker (defer it). No-op on failure so a client
// never fails to launch just because the registry couldn't be written.
func RegisterClient(key string, pid int, keyFile string) func() {
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
	// The profile's private key goes BESIDE the marker, never inside it. The
	// daemon holds one tunnel per cluster and knows a cluster only as host:port,
	// so it needs the key to dial with the client's identity rather than the
	// built-in one. But this file is read by whatever daemon is ALREADY RUNNING,
	// which may predate this code by any amount of time: a long-lived root daemon
	// survives launches. An older reader does TrimSpace over the whole marker, so
	// a second line inside it becomes part of the cluster key - it then dials a
	// host that does not exist, opens no tunnel, and every name resolves to a
	// fake IP with nothing behind it. A sidecar leaves the marker byte-identical
	// to what every released version writes, and is simply absent for readers
	// that do not know to look.
	if keyFile != "" {
		_ = os.WriteFile(marker+keyFileSuffix, []byte(keyFile), 0o644)
	}
	return func() {
		_ = os.Remove(marker)
		_ = os.Remove(marker + keyFileSuffix)
	}
}

// keyFileSuffix names the sidecar. Not a valid PID, so every existing scan that
// parses an entry name as a number skips it without being taught to.
const keyFileSuffix = ".key"

// ClusterKeyFile is the profile key a live client of this cluster registered,
// "" when none did. The daemon asks, because it dials on their behalf and the
// identity is theirs, not its own.
//
// First live marker wins. Two profiles pointing at the same host:port with
// different keys are the same cluster to the daemon, which holds one tunnel for
// it; picking either is what "one tunnel per cluster" already means, and the
// agent decides anyway.
func ClusterKeyFile(key string) string {
	entries, err := os.ReadDir(clientsDir(key))
	if err != nil {
		return ""
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || !processAlive(pid) {
			continue
		}
		if b, err := os.ReadFile(filepath.Join(clientsDir(key), e.Name()+keyFileSuffix)); err == nil {
			if kf := strings.TrimSpace(string(b)); kf != "" {
				return kf
			}
		}
	}
	return ""
}

// clusterForPID reports the cluster a registered launcher PID belongs to
// (PID→cluster, the reverse the multicluster router walk needs; see
// walkToCluster in pidroute.go) by finding its marker across the per-cluster
// client dirs and reading the key it carries. The fs scan is the source of
// truth; holders cache it.
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

// LiveClients counts client markers whose process is still alive, reaping
// stale ones on the way.
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
			_ = os.Remove(filepath.Join(clientsDir(key), e.Name()+keyFileSuffix))
		}
	}
	return n
}

// ActiveClusters returns the keys of clusters that currently have at least one
// live client, read from the per-cluster client dirs. The global datapath
// owner reconciles its tunnel set against this — open a tunnel for each active
// cluster, close the rest — so the registry doubles as the IPC: a `plug -p X`
// just registers, and cluster X is discovered. Stale markers are reaped on the
// way.
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
				// Reap the marker AND its sidecar, or a dead client's key path
				// outlives it and the daemon dials with a stale identity.
				_ = os.Remove(filepath.Join(dir, m.Name()))
				_ = os.Remove(filepath.Join(dir, m.Name()+keyFileSuffix))
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
