//go:build darwin

package tun

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

var graftDir = "/var/run/plug" // overridable in tests

// lockPath is the per-cluster leader lock file.
func lockPath(key string) string { return filepath.Join(graftDir, ClusterHash(key)+".lock") }

// backupPath is the per-cluster DNS backup file (the anti-crash net).
func backupPath(key string) string { return filepath.Join(graftDir, ClusterHash(key)+".dns.bak") }

// resolvBackupPath is the per-cluster /etc/resolv.conf snapshot (the anti-crash
// net for the resolv.conf override, alongside backupPath's scutil snapshot).
func resolvBackupPath(key string) string {
	return filepath.Join(graftDir, ClusterHash(key)+".resolv.bak")
}

// setupBackupPath is the per-cluster snapshot of the MANUAL DNS dict
// (Setup:/Network/Service/<svc>/DNS) — Setup: is persistent, so a crashed daemon
// must never leave the user's manual DNS pointing at a dead resolver.
func setupBackupPath(key string) string {
	return filepath.Join(graftDir, ClusterHash(key)+".setup.bak")
}

// readyPath marks that the global daemon holds a LIVE tunnel for this cluster.
func readyPath(key string) string { return filepath.Join(graftDir, ClusterHash(key)+".ready") }

// SharedKnownHosts is Windows-only (its SYSTEM service needs a user-writable TOFU
// file). On macOS the daemon pins under the user's ~/.plug and chowns it back, so
// this returns "" and dialTunnel keeps that path.
func SharedKnownHosts() string { return "" }

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
