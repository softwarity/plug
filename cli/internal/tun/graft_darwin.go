//go:build darwin

package tun

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

var graftDir = "/var/run/plug" // overridable in tests

// AcquireCluster coordinates per-cluster datapath ownership on macOS, where the
// system resolver is GLOBAL (one entry for the whole machine). The FIRST `plug`
// for a given cluster becomes the LEADER — it owns the utun, the scutil DNS
// repoint and the tunnel; concurrent `plug`s for the SAME cluster GRAFT onto it
// (they just run the child, which reaches the cluster through the leader's
// datapath). The leader holds an exclusive flock for its whole lifetime; a
// grafter simply fails to take it. release() drops the lock for the leader and is
// a no-op for a grafter.
//
// NOTE: the leader owns the datapath, so if it exits before the grafters they
// lose resolution. In practice the first-launched service is the last killed, so
// this is acceptable — and documented. (A daemon would remove the caveat; that's
// deferred until there's a real need.)
func AcquireCluster(key string) (leader bool, release func(), err error) {
	if e := os.MkdirAll(graftDir, 0o755); e != nil {
		return true, func() {}, nil // can't coordinate → behave as a standalone leader
	}
	sum := sha1.Sum([]byte(key))
	path := filepath.Join(graftDir, hex.EncodeToString(sum[:8])+".lock")
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if e != nil {
		return true, func() {}, nil
	}
	if e := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close() // held by another process → a leader for this cluster is already up
		return false, func() {}, nil
	}
	// We are the leader: keep the fd open (that holds the lock) until release.
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte("pid="+strconv.Itoa(os.Getpid())+" key="+key+"\n"), 0)
	return true, func() { f.Close() }, nil
}
