package tun

import (
	"math"
	"sync"
)

// --- Multicluster: attributing an intercepted connection to a cluster ---------
//
// The validated design (see docs/multicluster.md) routes by PID AT CONNECT, not
// at DNS: one system resolver, fake IPs minted per NAME (shared across clusters),
// and when the app connect()s a fake IP the daemon attributes the flow to a
// cluster by walking the connecting process's parent chain up to the `plug -p X`
// launcher that started it. Bare names stay transparent; a process we cannot
// attribute (detached via setsid, or a chain broken by PID recycling) is refused
// — "refuse en cas de doute" (a hard RST, never a wrong-cluster route).
//
// This file is the attribution CORE, kept pure so it is fully unit-tested. On
// macOS it is LIVE: multiDial (router_darwin.go) feeds it into the global daemon's
// datapath, validated on two real clusters. Windows shares this core and the
// per-OS primitives (pidroute_windows.go) but not yet the N-tunnel daemon — that
// SYSTEM service is the remaining step; the single-cluster path calls none of this.

// The multicluster case is the only one left with a type: a clusterRouter
// interface and a staticRouter that always answered its one key used to sit here,
// the second being "the single-cluster case, which preserves today's behaviour
// untouched". Neither was ever instantiated, not even by a test: multiDial holds
// a pidRouter directly and takes its own shortcut when one cluster is up. An
// interface with one implementer is a seam that seams nothing.

// pidRouter is the multicluster case: source port → owning PID (OS socket table)
// → parent chain → the registered `plug -p X` launcher → its cluster key.
type pidRouter struct {
	// clusterForPID reports the cluster a live launcher PID belongs to, backed by
	// the daemon's client registry (registry_*.go). Absent ⇒ not a launcher.
	clusterForPID func(pid int) (string, bool)
	// pidForConn maps a local source port to the PID owning that socket, read from
	// the OS TCP table (pidForLocalPort, per-OS). Absent ⇒ socket gone/foreign.
	pidForConn func(srcPort uint16) (int, bool)
	// ppidOf returns pid's parent (per-OS: /proc, sysctl, toolhelp).
	ppidOf func(pid int) (int, bool)
	// startOf returns pid's creation stamp (per-OS unit, monotonic within a boot).
	// The walk uses it to reject a recycled PID: a parent cannot have started AFTER
	// its child, so a younger "ancestor" is a stale number, not a real forebear.
	// Absent ⇒ refuse (in doubt, no route). This matters most on Windows, which —
	// unlike unix — does not re-parent orphans, so a dead parent's PID lingers in
	// the child and may already name a younger, unrelated process.
	startOf func(pid int) (int64, bool)

	// seen remembers what the walk found, per process. Nil disables it entirely,
	// which is what the pure unit tests of walkToCluster want.
	seen *pidCache
}

// pidCache remembers the CLUSTER a process belongs to, so the ancestry is climbed
// once per process instead of once per connection.
//
// Why it is worth having, measured rather than assumed: each hop costs two forks
// (`ps -o lstart=` and `ps -o ppid=`), about 16ms each on a real Mac, and a
// shell → plug → app chain is three hops. A database pool opening ten connections
// paid that five-fork walk ten times for an answer that had not changed.
//
// What it must NOT remember is the recycled-PID check. A PID reused by an
// unrelated process is the exact thing walkToCluster refuses on, and caching a
// route past it would send someone's traffic into another cluster - the one
// failure this design will not accept. So the START STAMP is re-read on every
// flow (one fork, not five) and the entry is only trusted when it matches.
//
// Refusals are not cached either: the launcher registry is written by a client
// that may not have registered yet when its first flow lands, and remembering
// "no" would make that a permanent no.
type pidCache struct {
	mu sync.Mutex
	m  map[int]cachedRoute
}

type cachedRoute struct {
	start int64  // what startOf said when the walk ran
	key   string // the cluster it led to
}

// pidCacheMax bounds the map. Processes come and go, and a daemon that runs for
// weeks must not accumulate one entry per PID ever seen. Cleared wholesale rather
// than evicted one by one: a re-walk costs five forks and happens once per live
// process, which is cheaper than tracking recency.
const pidCacheMax = 1024

func newPidCache() *pidCache { return &pidCache{m: map[int]cachedRoute{}} }

func (c *pidCache) get(pid int, start int64) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[pid]
	if !ok || e.start != start {
		return "", false
	}
	return e.key, true
}

func (c *pidCache) put(pid int, start int64, key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= pidCacheMax {
		c.m = map[int]cachedRoute{}
	}
	c.m[pid] = cachedRoute{start: start, key: key}
}

// maxAncestry bounds the parent-chain walk — a guard against a cycle or a runaway
// chain; real ancestries (shell → plug → app → child) are only a few hops.
const maxAncestry = 64

func (r pidRouter) route(srcPort uint16) (string, bool) {
	pid, ok := r.pidForConn(srcPort)
	if !ok {
		return "", false // socket vanished or unattributable — refuse
	}
	if r.seen == nil {
		return walkToCluster(pid, r.ppidOf, r.startOf, r.clusterForPID)
	}
	// The stamp is read on EVERY flow, and it is the only thing that is. It is
	// what tells a remembered answer from a PID that now names a stranger, and it
	// costs one fork where the walk costs five.
	start, ok := r.startOf(pid)
	if !ok {
		return "", false // no stamp, no route: in doubt, refuse
	}
	if key, hit := r.seen.get(pid, start); hit {
		return key, true
	}
	key, ok := walkToCluster(pid, r.ppidOf, r.startOf, r.clusterForPID)
	if ok {
		r.seen.put(pid, start, key)
	}
	return key, ok
}

// walkToCluster climbs pid's ancestry until an ancestor is a registered launcher
// and returns its cluster. Pure — the injected funcs make it fully testable. It
// refuses (ok=false) rather than guessing at init (pid<=1), on a broken or
// self-referential chain, or after maxAncestry hops.
//
// It also refuses a TEMPORALLY IMPOSSIBLE chain: startOf stamps each hop, and a
// parent that started after its child is a recycled PID (same number, new,
// unrelated process), not a real forebear — so a younger ancestor aborts the walk
// instead of misrouting the flow to whatever cluster that stranger belongs to.
// prev seeds at +inf so the origin itself is never rejected; a hop with no stamp
// refuses (in doubt, no route).
func walkToCluster(pid int, ppidOf func(int) (int, bool), startOf func(int) (int64, bool), clusterForPID func(int) (string, bool)) (string, bool) {
	prev := int64(math.MaxInt64) // the origin has no child to be younger than
	for i := 0; pid > 1 && i < maxAncestry; i++ {
		start, ok := startOf(pid)
		if !ok {
			return "", false
		}
		if start > prev {
			return "", false // an ancestor younger than its child ⇒ recycled PID
		}
		if key, ok := clusterForPID(pid); ok {
			return key, true
		}
		ppid, ok := ppidOf(pid)
		if !ok || ppid == pid || ppid <= 0 {
			return "", false
		}
		prev, pid = start, ppid
	}
	return "", false
}
