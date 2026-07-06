package tun

// --- Multicluster: attributing an intercepted connection to a cluster ---------
//
// The validated design (see docs/multicluster.md) routes by PID AT CONNECT, not
// at DNS: one system resolver, fake IPs minted per NAME (shared across clusters),
// and when the app connect()s a fake IP the daemon attributes the flow to a
// cluster by walking the connecting process's parent chain up to the `plug -p X`
// launcher that started it. Bare names stay transparent; the only refusal is a
// process we cannot attribute (e.g. detached via setsid) — "refuse en cas de
// doute" (a hard RST, never a wrong-cluster route).
//
// This file is the attribution CORE. It is intentionally NOT yet wired into
// handleTCP — that, plus the N-tunnel daemon, is the next step, to be validated
// on two real clusters. Everything here compiles, is unit-tested where pure, and
// touches no live datapath, so the proven single-cluster path is unaffected.

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
}

// maxAncestry bounds the parent-chain walk — a guard against a cycle or a runaway
// chain; real ancestries (shell → plug → app → child) are only a few hops.
const maxAncestry = 64

func (r pidRouter) route(srcPort uint16) (string, bool) {
	pid, ok := r.pidForConn(srcPort)
	if !ok {
		return "", false // socket vanished or unattributable — refuse
	}
	return walkToCluster(pid, r.ppidOf, r.clusterForPID)
}

// walkToCluster climbs pid's ancestry until an ancestor is a registered launcher
// and returns its cluster. Pure — the injected funcs make it fully testable. It
// stops at init (pid<=1), on a broken or self-referential chain, or after
// maxAncestry hops, refusing (ok=false) rather than guessing.
func walkToCluster(pid int, ppidOf func(int) (int, bool), clusterForPID func(int) (string, bool)) (string, bool) {
	for i := 0; pid > 1 && i < maxAncestry; i++ {
		if key, ok := clusterForPID(pid); ok {
			return key, true
		}
		ppid, ok := ppidOf(pid)
		if !ok || ppid == pid || ppid <= 0 {
			return "", false
		}
		pid = ppid
	}
	return "", false
}
