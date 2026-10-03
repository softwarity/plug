//go:build darwin || windows

package tun

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The client registry, shared by the macOS daemon and the Windows service (the
// per-OS bits are processAlive and markerOwnedByProcess, in registry_<os>.go
// and account_<os>.go). Each `plug <cmd>`
// drops a PID marker carrying its cluster key under graftDir; the global
// datapath owner (daemon / SYSTEM service) counts the LIVE ones to know which
// clusters are active and when to shut down. A file registry (not a pipe)
// lets a client register WITHOUT elevation, and liveness is checked — not just
// presence — so a client killed with -9 never wedges the count (the exact
// "kill and relaunch 10×/h" case). Linux needs none of this: each launch owns
// its private datapath via its mount namespace.

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
	_ = os.WriteFile(marker+uidFileSuffix, []byte(thisAccount()), 0o644)
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
	return func() { reapMarker(dir, pid) }
}

// reapMarker removes a client's marker and every sidecar beside it. One list,
// in one place: the three readers that reaped used to carry their own, none of
// them complete, and a .pins left behind became active again the day the kernel
// handed its pid number to something else.
func reapMarker(dir string, pid int) {
	base := filepath.Join(dir, strconv.Itoa(pid))
	for _, suffix := range []string{"", keyFileSuffix, uidFileSuffix, startFileSuffix, pinsFileSuffix} {
		_ = os.Remove(base + suffix)
	}
}

// liveMarker reads the marker of pid in dir and answers the cluster key it
// carries, or nothing when the marker is not to be trusted. This is the ONE
// place that decides what a trustworthy marker is, and every reader goes
// through it, because the registry lives in a directory that is not plug's
// alone: on Windows every account can write %ProgramData%\plug, and on both
// platforms a crashed client leaves its files behind for the kernel to give
// its pid number to a stranger.
//
// Three things have to hold:
//
//   - The directory is named after the key the marker carries. A marker is
//     found by scanning *.clients, and a directory named by hand could carry
//     any key at all: "route this pid into that cluster", chosen by whoever
//     wrote it. The name is the only part the writer cannot pick freely, since
//     RegisterClient derives it from the key.
//   - The process is still the one that registered (markerStillItsProcess). A
//     pid alone says "alive"; a pid plus its start stamp says "the same". A
//     marker whose process is gone, or whose number now names something else,
//     is reaped here, so a dead launcher's pins and key do not outlive it.
//   - The file was written by the account the process runs under, where the
//     operating system can say so (markerOwnedByProcess, Windows). Without
//     it, a marker named with somebody else's pid would send their traffic
//     into the writer's cluster.
func liveMarker(dir string, pid int) (string, bool) {
	b, err := os.ReadFile(filepath.Join(dir, strconv.Itoa(pid)))
	if err != nil {
		return "", false
	}
	key := strings.TrimSpace(string(b))
	if key == "" || filepath.Base(dir) != ClusterHash(key)+".clients" {
		return "", false
	}
	if !markerStillItsProcess(dir, pid) {
		reapMarker(dir, pid)
		return "", false
	}
	if !markerOwnedByProcess(filepath.Join(dir, strconv.Itoa(pid)), pid) {
		return "", false
	}
	return key, true
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

// clientAccounts is the set of accounts with a live client on this cluster, as
// each client wrote itself: a decimal uid on macOS, a SID on Windows. Empty when
// nothing registered one, which is what a client older than this code produces:
// callers must read that as "unknown", never as "nobody".
func clientAccounts(key string) map[string]bool {
	out := map[string]bool{}
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
		if err != nil {
			continue
		}
		if _, ok := liveMarker(clientsDir(key), pid); !ok {
			continue
		}
		b, err := os.ReadFile(filepath.Join(clientsDir(key), name))
		if err != nil {
			continue
		}
		if account := strings.TrimSpace(string(b)); account != "" {
			out[account] = true
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
		if err != nil {
			continue
		}
		if _, ok := liveMarker(clientsDir(key), pid); !ok {
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
// truth; holders cache it. Only a marker liveMarker vouches for counts: this
// is the answer that sends a flow into a tunnel, and a marker left by a dead
// launcher, or written by hand under a directory of someone's choosing, must
// not be the thing that decides which one.
func clusterForPID(pid int) (string, bool) {
	entries, err := os.ReadDir(graftDir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.HasSuffix(e.Name(), ".clients") {
			continue
		}
		if key, ok := liveMarker(filepath.Join(graftDir, e.Name()), pid); ok {
			return key, true
		}
	}
	return "", false
}

// LiveClients counts client markers whose process is still the one that
// registered, reaping stale ones on the way.
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
		if _, ok := liveMarker(clientsDir(key), pid); ok {
			n++
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
			// liveMarker reaps a dead client's marker AND its sidecars, or its
			// key path outlives it and the daemon dials with a stale identity.
			// Every marker is visited, not only the ones before the first live
			// one, so the reaping does not depend on directory order.
			if k, ok := liveMarker(dir, pid); ok && key == "" {
				key = k
			}
		}
		if key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

// ClusterHeldByOther reports the account already holding this cluster when it is
// not me, so a launcher can refuse before it registers.
//
// This is where the ownership question belongs, and it took a second look to see
// it. The check added first was per FLOW, in the single-cluster shortcut: it asked
// who owned the socket and compared against everyone with a live marker. Two holes
// followed from that. A client registers BEFORE it authenticates, so a second
// account had only to run `plug -p <someone-else's-cluster>` to put its own
// identity in that set and be waved through the tunnel somebody else's key had
// opened. And the MULTICLUSTER path never asked at all: it walks the ancestry to
// the registered launcher and hands over that launcher's cluster, owner unread.
//
// Refusing the REGISTRATION closes both, because both are downstream of it: no
// marker, no membership and no ancestor to walk to. It also costs nothing per
// connection and can say who, where a flow check can only send a RST that the
// application reports as a connection reset by an unnamed peer.
//
// THE SCOPE IS ONE MACHINE, and this is the sentence to read before worrying
// about a team. Everything here comes from a local directory, /var/run/plug or
// %ProgramData%\plug; nothing is asked of the agent or of the network. Ten
// developers on ten machines share a cluster exactly as before, each with their
// own key and their own tunnel, and none of them can see that the others exist.
// What is refused is two accounts of the SAME computer holding one cluster at the
// same time, and only for as long as the first session lives.
//
// accountHolds carries what an account is per platform, and what cannot be one:
// root and LocalSystem are exempt, since they already own the machine, and
// anything that names nobody must not hold a cluster against anybody.
func ClusterHeldByOther(key, me string) (string, bool) {
	if !accountHolds(me) {
		return "", false
	}
	for other := range clientAccounts(key) {
		if other != me && accountHolds(other) {
			return other, true
		}
	}
	return "", false
}

// ThisAccount is thisAccount for the launcher, which asks the question in package
// main and must spell the answer the same way the marker does.
func ThisAccount() string { return thisAccount() }

// ClusterHeldRefusal is what the launcher prints, kept here beside the rule it
// states so the two cannot drift. It names the account, because "in use" without
// a who sends people looking at the cluster instead of at their own machine.
const ClusterHeldRefusal = "error: cluster %s is in use by another account on THIS machine (%s) - " +
	"plug gives one cluster to one account at a time, so its tunnel is never shared between them; " +
	"wait for that session to end. Other machines are unaffected: a cluster is shared by a team as before"

// markerStillItsProcess reports whether pid is not merely alive but is still the
// process that registered. A crashed client leaves its marker behind, and the
// kernel reissues pid numbers: without the stamp, the first unrelated process to
// land on that number resurrects a membership nobody holds.
//
// No stamp means a client older than startFileSuffix wrote the marker, and there
// the answer stays what it always was: alive is good enough. Refusing on a
// missing stamp would turn a version skew into a lockout, and this file already
// takes the opposite side of that trade twice.
//
// A "yes" is remembered for stampVerdictTTL, keyed on the marker AND the stamp
// it carried. On macOS procStart is a fork of ps, about 16ms, and the daemon's
// reconcile loop reads ActiveClusters three times a second: unremembered, three
// clients cost it a seventh of a core to re-learn what it knew a moment ago. A
// "no" is never remembered, since the caller reaps the marker on it; a marker
// rewritten with a new stamp has a new key and is asked afresh.
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
	verdictKey := filepath.Join(dir, strconv.Itoa(pid)) + "@" + strconv.FormatInt(want, 10)
	stampMu.Lock()
	at, seen := stampVerdicts[verdictKey]
	stampMu.Unlock()
	if seen && time.Since(at) < stampVerdictTTL {
		return true
	}
	start, ok := procStart(pid)
	if ok && start != want {
		return false
	}
	stampMu.Lock()
	if len(stampVerdicts) >= pidCacheMax {
		stampVerdicts = map[string]time.Time{} // bounded the way pidCache is: cleared, not evicted
	}
	stampVerdicts[verdictKey] = time.Now()
	stampMu.Unlock()
	return true
}

const stampVerdictTTL = 2 * time.Second

var (
	stampMu       sync.Mutex
	stampVerdicts = map[string]time.Time{}
)

// pinsFileSuffix names the sidecar listing the cluster names a client PINNED
// to its cluster, one per line: names only that client's session can have
// minted (a live-mount helper's), which the router then attributes to the
// cluster without an ancestry walk. Its own suffix, like the others, so an
// older daemon never reads it as a pid.
const pinsFileSuffix = ".pins"

// PinName records name as belonging to pid's cluster (key) for as long as the
// client is registered; the sidecar goes with the marker. Appends, so a
// session with several mounts lists them all.
func PinName(key string, pid int, name string) {
	file := filepath.Join(clientsDir(key), strconv.Itoa(pid)+pinsFileSuffix)
	f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	_, _ = f.WriteString(strings.ToLower(name) + "\n")
	f.Close()
}

// UnpinNames forgets every name pid pinned.
func UnpinNames(key string, pid int) {
	_ = os.Remove(filepath.Join(clientsDir(key), strconv.Itoa(pid)+pinsFileSuffix))
}

// pinnedCluster answers the cluster a name is pinned to by a LIVE client, or
// nothing. Scans the registry (a directory per cluster, a small file per
// pinning client); a per-name verdict is kept for a few seconds, since a
// mount opens many flows in a row.
//
// A pin is honoured on the same terms as the marker it rides beside
// (liveMarker), and on Windows the pins file itself must be the process's own
// (markerOwnedByProcess). This answer is taken BEFORE any account check,
// because the kernel's SMB client is nobody's account; it is therefore the one
// a hand-written file could abuse most cheaply, and the one that must trust
// the least.
func pinnedCluster(name string) (string, bool) {
	name = strings.ToLower(name)
	pinMu.Lock()
	if e, ok := pinCache[name]; ok && time.Since(e.at) < 5*time.Second {
		pinMu.Unlock()
		return e.key, e.key != ""
	}
	pinMu.Unlock()
	key := ""
	dirs, _ := filepath.Glob(filepath.Join(graftDir, "*.clients"))
scan:
	for _, dir := range dirs {
		pins, _ := filepath.Glob(filepath.Join(dir, "*"+pinsFileSuffix))
		for _, file := range pins {
			pid, err := strconv.Atoi(strings.TrimSuffix(filepath.Base(file), pinsFileSuffix))
			if err != nil {
				continue
			}
			k, ok := liveMarker(dir, pid)
			if !ok || !markerOwnedByProcess(file, pid) {
				continue
			}
			b, err := os.ReadFile(file)
			if err != nil {
				continue
			}
			for _, line := range strings.Split(string(b), "\n") {
				if strings.TrimSpace(line) == name {
					key = k
					break scan
				}
			}
		}
	}
	pinMu.Lock()
	pinCache[name] = pinEntry{key: key, at: time.Now()}
	pinMu.Unlock()
	return key, key != ""
}

type pinEntry struct {
	key string
	at  time.Time
}

var (
	pinMu    sync.Mutex
	pinCache = map[string]pinEntry{}
)
