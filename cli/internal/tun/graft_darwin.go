//go:build darwin

package tun

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

var graftDir = "/var/run/plug" // overridable in tests

// ClusterHash is the short, filesystem-safe id for a cluster key (host:port).
func ClusterHash(key string) string {
	sum := sha1.Sum([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// lockPath is the per-cluster leader lock file.
func lockPath(key string) string { return filepath.Join(graftDir, ClusterHash(key)+".lock") }

// backupPath is the per-cluster DNS backup file (the anti-crash net).
func backupPath(key string) string { return filepath.Join(graftDir, ClusterHash(key)+".dns.bak") }

// resolvBackupPath is the per-cluster /etc/resolv.conf snapshot (the anti-crash
// net for the resolv.conf override, alongside backupPath's scutil snapshot).
func resolvBackupPath(key string) string {
	return filepath.Join(graftDir, ClusterHash(key)+".resolv.bak")
}

// readyPath marks that the global daemon holds a LIVE tunnel for this cluster.
func readyPath(key string) string { return filepath.Join(graftDir, ClusterHash(key)+".ready") }

// SharedKnownHosts is Windows-only (its SYSTEM service needs a user-writable TOFU
// file). On macOS the daemon pins under the user's ~/.plug and chowns it back, so
// this returns "" and dialTunnel keeps that path.
func SharedKnownHosts() string { return "" }

// MarkClusterReady / UnmarkClusterReady are called by the daemon's reconcile loop
// when a cluster's tunnel opens / closes. ClusterReady lets a `plug -p X <cmd>`
// wait for its own tunnel before running: the datapath is up daemon-wide, but the
// per-cluster tunnel opens on the next reconcile after the client registers.
func MarkClusterReady(key string)   { _ = os.WriteFile(readyPath(key), nil, 0o644) }
func UnmarkClusterReady(key string) { _ = os.Remove(readyPath(key)) }
func ClusterReady(key string) bool  { _, err := os.Stat(readyPath(key)); return err == nil }

// errorPath records WHY the daemon could not open this cluster's tunnel (agent
// unreachable, host key changed…). Written on a failed reconcile, cleared on
// success; a launcher waiting past its timeout shows it instead of a blank
// "not ready". Mirrors graft_windows.go.
func errorPath(key string) string { return filepath.Join(graftDir, ClusterHash(key)+".error") }

func MarkClusterError(key, msg string) { _ = os.WriteFile(errorPath(key), []byte(msg), 0o644) }
func ClearClusterError(key string)     { _ = os.Remove(errorPath(key)) }
func ClusterError(key string) string {
	b, err := os.ReadFile(errorPath(key))
	if err != nil {
		return ""
	}
	return string(b)
}

// AcquireCluster coordinates per-cluster datapath ownership on macOS, where the
// system resolver is GLOBAL (one entry machine-wide). The holder of the exclusive
// flock is the LEADER that owns the utun + scutil DNS repoint + tunnel; anyone who
// fails to take it grafts onto that datapath. release() drops the lock for the
// leader and is a no-op otherwise. Once the daemon exists it is the leader (it
// holds the lock for its whole life); `plug <cmd>` only probes via DaemonAlive.
func AcquireCluster(key string) (leader bool, release func(), err error) {
	if e := os.MkdirAll(graftDir, 0o755); e != nil {
		return true, func() {}, nil // can't coordinate → behave as a standalone leader
	}
	f, e := os.OpenFile(lockPath(key), os.O_CREATE|os.O_RDWR, 0o644)
	if e != nil {
		return true, func() {}, nil
	}
	if e := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close() // held by another process → a leader is already up
		return false, func() {}, nil
	}
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte("pid="+strconv.Itoa(os.Getpid())+" key="+key+"\n"), 0)
	return true, func() { f.Close() }, nil
}

// DaemonAlive reports whether a daemon already holds this cluster's lock (i.e. a
// datapath is up). It probes the flock WITHOUT disturbing it: if it can take the
// lock, nobody holds it, so it releases immediately and returns false.
func DaemonAlive(key string) bool {
	f, e := os.OpenFile(lockPath(key), os.O_CREATE|os.O_RDWR, 0o644)
	if e != nil {
		return false
	}
	defer f.Close()
	if e := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return true // held → a daemon owns the datapath
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) // we're not the owner — release at once
	return false
}

// DaemonPID reads the leader PID recorded in the lock file (written by
// AcquireCluster). Returns 0 if unknown. Used by `plug down`.
func DaemonPID(key string) int {
	b, err := os.ReadFile(lockPath(key))
	if err != nil {
		return 0
	}
	for _, tok := range strings.Fields(string(b)) {
		if v, ok := strings.CutPrefix(tok, "pid="); ok {
			if pid, err := strconv.Atoi(v); err == nil {
				return pid
			}
		}
	}
	return 0
}
