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
	// And WHO this client is, in its own sidecar for the same reason. The daemon
	// is machine-wide: on macOS it repoints the primary network service's
	// resolver, so any process on the box, under any account, can resolve a
	// cluster name and connect to the fake IP that comes back. With two clusters
	// up, the ancestry walk already refuses a flow it cannot attribute. With ONE,
	// the router takes a shortcut and hands the tunnel to whoever asked, which is
	// how a second local account reached another user's cluster services by
	// typing a name.
	//
	// os.Getuid, not Geteuid: the launcher is setuid root on macOS, so the
	// effective uid is 0 and the real one is the person whose cluster this is,
	// which is also the uid the dropped child's flows will carry.
	_ = os.WriteFile(marker+uidFileSuffix, []byte(strconv.Itoa(os.Getuid())), 0o644)
	// And WHEN it started, in a third sidecar for the same compatibility reason.
	// A pid alone says "alive"; a pid plus a start time says "still the same
	// process". That distinction did not matter while this marker only GRANTED
	// membership, because a recycled pid could at worst let a stranger in through
	// a cluster they would still have to name. It matters now that the same
	// marker REFUSES: without it, a client that crashed without unregistering
	// locks its own account out of its own cluster as soon as the kernel hands
	// that pid number to anything else.
	if start, ok := procStart(pid); ok {
		_ = os.WriteFile(marker+startFileSuffix, []byte(strconv.FormatInt(start, 10)), 0o644)
	}
	return func() {
		_ = os.Remove(marker)
		_ = os.Remove(marker + keyFileSuffix)
		_ = os.Remove(marker + uidFileSuffix)
		_ = os.Remove(marker + startFileSuffix)
	}
}

// uidFileSuffix names the owner sidecar, like keyFileSuffix and for the same
// reason: not a valid PID, so every existing scan that parses an entry name as a
// number skips it without being taught to, and a daemon that predates this code
// never sees it.
const uidFileSuffix = ".uid"

// startFileSuffix names the sidecar carrying the client's start time. Its own
// file rather than a second line in the .uid one, because a reader older than
// this code does TrimSpace over that whole file and would parse "501\n1757..."
// as no uid at all - which, on the path that refuses, reads as "nobody holds this
// cluster" and silently gives the check away. A sidecar it does not know about is
// simply not read.
const startFileSuffix = ".start"

// clientUIDs is the set of accounts with a live client on this cluster. Empty
// when nothing registered one, which is what a client older than this code
// produces: callers must read that as "unknown", never as "nobody".
func clientUIDs(key string) map[int]bool {
	out := map[int]bool{}
	entries, err := os.ReadDir(clientsDir(key))
	if err != nil {
		return out
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, uidFileSuffix) {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSuffix(name, uidFileSuffix))
		if err != nil || !markerStillItsProcess(clientsDir(key), pid) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(clientsDir(key), name))
		if err != nil {
			continue
		}
		if uid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
			out[uid] = true
		}
	}
	return out
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
	kf, _ := ClusterKeyFileFrom(key)
	return kf
}

// ClusterKeyFileFrom also names the MARKER the key came from, which is what lets
// a caller ask the operating system who registered that client. The path inside
// the sidecar was written by the client and says only what the client chose to
// say; the marker's ownership is recorded by the system and cannot be claimed.
// On Windows that difference is the whole guard, since the daemon there runs as
// the machine account and would otherwise open any file a user named.
func ClusterKeyFileFrom(key string) (keyFile, marker string) {
	entries, err := os.ReadDir(clientsDir(key))
	if err != nil {
		return "", ""
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || !processAlive(pid) {
			continue
		}
		m := filepath.Join(clientsDir(key), e.Name())
		if b, err := os.ReadFile(m + keyFileSuffix); err == nil {
			if kf := strings.TrimSpace(string(b)); kf != "" {
				return kf, m
			}
		}
	}
	return "", ""
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
			_ = os.Remove(filepath.Join(clientsDir(key), e.Name()+uidFileSuffix))
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

// ClusterHeldByOther reports the account already holding this cluster when it is
// not uid's, so a launcher can refuse before it registers.
//
// This is where the ownership question belongs, and it took a second look to see
// it. The check added first was per FLOW, in the single-cluster shortcut: it asked
// who owned the socket and compared against everyone with a live marker. Two holes
// followed from that. A client registers BEFORE it authenticates, so a second
// account had only to run `plug -p <someone-else's-cluster>` to put its own uid in
// that set and be waved through the tunnel somebody else's key had opened. And the
// MULTICLUSTER path never asked at all: it walks the ancestry to the registered
// launcher and hands over that launcher's cluster, uid unread.
//
// Refusing the REGISTRATION closes both, because both are downstream of it: no
// marker, no membership and no ancestor to walk to. It also costs nothing per
// connection and can say why, where a flow check can only send a RST that the
// application reports as a connection reset by an unnamed peer.
//
// uid 0 is exempt, as it was in the flow check: root already owns the machine, and
// the daemon's own probes run there.
//
// Note what changed in kind, not just in degree: the same set that used to GRANT
// now REFUSES, so a marker that is wrong no longer merely lets someone in, it
// keeps the rightful owner out. Liveness here is a pid, and a pid can be recycled
// onto an unrelated process, which would make a crashed client's marker look alive
// and hold the cluster against its own account. Two things keep that narrow: a
// clean exit removes the marker, and ActiveClusters reaps dead ones three times a
// second for as long as a daemon runs. Stamping the marker with a start time, the
// way the ancestry walk already does, is what would close it properly.
//
// WINDOWS IS NOT COVERED, and saying so here is the point of this paragraph.
// os.Getuid returns -1 for every process there, so every marker records the same
// owner and there is nobody to tell apart; pidroute_windows.go's uidOf reports
// failure for the same reason, which already made the flow check fall through. A
// negative uid is refused as an answer rather than compared, so this returns "not
// held" instead of quietly returning "held by -1" for the second account. Covering
// Windows means giving a client a real identity to record (its token's SID), and
// that is a different piece of work than this one.
func ClusterHeldByOther(key string, uid int) (int, bool) {
	if uid <= 0 {
		return 0, false
	}
	for other := range clientUIDs(key) {
		if other != uid && other != 0 {
			return other, true
		}
	}
	return 0, false
}

// ClusterHeldRefusal is what the launcher prints, kept here beside the rule it
// states so the two cannot drift. It names the account, because "in use" without
// a who sends people looking at the cluster instead of at their own machine.
const ClusterHeldRefusal = "error: cluster %s is in use by another account on this machine (uid %d) - " +
	"plug gives one cluster to one account, so its tunnel is never shared; wait for that session to end"

// markerStillItsProcess reports whether pid is not merely alive but is still the
// process that registered. A crashed client leaves its marker behind, and the
// kernel reissues pid numbers: without the stamp, the first unrelated process to
// land on that number resurrects a membership nobody holds.
//
// No stamp means a client older than startFileSuffix wrote the marker, and there
// the answer stays what it always was: alive is good enough. Refusing on a
// missing stamp would turn a version skew into a lockout, and this file already
// takes the opposite side of that trade twice.
func markerStillItsProcess(dir string, pid int) bool {
	if !processAlive(pid) {
		return false
	}
	b, err := os.ReadFile(filepath.Join(dir, strconv.Itoa(pid)+startFileSuffix))
	if err != nil {
		return true
	}
	want, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return true
	}
	start, ok := procStart(pid)
	return !ok || start == want
}
