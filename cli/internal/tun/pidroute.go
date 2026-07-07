package tun

import "math"

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

// clusterRouter maps an intercepted flow (identified by its source port) to the
// cluster key it must be spliced to, or ok=false to refuse it. handleTCP will
// hold one once wired; a single-cluster daemon uses a trivial router that always
// returns its one key (no PID lookup, zero regression).
type clusterRouter interface {
	route(srcPort uint16) (clusterKey string, ok bool)
}

// staticRouter is the single-cluster case: every flow goes to the one cluster,
// no attribution needed. This is what preserves today's behaviour untouched.
type staticRouter struct{ key string }

func (s staticRouter) route(uint16) (string, bool) { return s.key, true }

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
}

// maxAncestry bounds the parent-chain walk — a guard against a cycle or a runaway
// chain; real ancestries (shell → plug → app → child) are only a few hops.
const maxAncestry = 64

func (r pidRouter) route(srcPort uint16) (string, bool) {
	pid, ok := r.pidForConn(srcPort)
	if !ok {
		return "", false // socket vanished or unattributable — refuse
	}
	return walkToCluster(pid, r.ppidOf, r.startOf, r.clusterForPID)
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
