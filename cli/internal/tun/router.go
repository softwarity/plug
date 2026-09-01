//go:build darwin || windows

package tun

import "sync"

// multiDial builds the global daemon's dialFunc. With a SINGLE active cluster
// there is no ambiguity, so it routes transparently to that one tunnel — exactly
// like the single-cluster datapath (no PID lookup, nothing to refuse), which keeps
// that case a non-regression, including detached children. With TWO OR MORE it
// attributes each flow: the app's source port → the PID owning the socket (lsof) →
// walk its ancestry (ps) → the registered `plug -p X` launcher (clusterForPID) →
// that cluster's tunnel, refusing (RST) a flow it can't attribute (e.g. setsid).
func multiDial(ct *ClusterTransports) dialFunc {
	r := pidRouter{
		clusterForPID: clusterForPID,
		pidForConn:    pidForLocalPort,
		ppidOf:        ppidOf,
		startOf:       procStart,
		seen:          newPidCache(),
	}
	return func(srcPort uint16) (Dialer, string, bool) {
		if d, key, ok := ct.sole(); ok {
			if !soleAllows(srcPort, r.pidForConn, uidOf, clientUIDs(key)) {
				return nil, "", false
			}
			return d, key, true // one cluster → transparent, like constDial
		}
		key, ok := r.route(srcPort)
		if !ok {
			return nil, "", false
		}
		d, ok := ct.get(key)
		return d, key, ok
	}
}

// ClusterTransports is the global daemon's live tunnel set: cluster key → its SSH
// transport. multiDial reads it to route each attributed flow; the daemon's
// reconcile loop fills and prunes it. Concurrency-safe.
type ClusterTransports struct {
	mu sync.Mutex
	m  map[string]Dialer
}

// NewClusterTransports returns an empty transport set.
func NewClusterTransports() *ClusterTransports { return &ClusterTransports{m: map[string]Dialer{}} }

// Has reports whether a transport is already held for key.
func (c *ClusterTransports) Has(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.m[key]
	return ok
}

// Set installs (or replaces) the transport for key.
func (c *ClusterTransports) Set(key string, d Dialer) {
	c.mu.Lock()
	c.m[key] = d
	c.mu.Unlock()
}

// Remove drops key and returns its transport (for the caller to Close), if any.
func (c *ClusterTransports) Remove(key string) (Dialer, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.m[key]
	delete(c.m, key)
	return d, ok
}

// Keys returns the cluster keys currently held.
func (c *ClusterTransports) Keys() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	ks := make([]string, 0, len(c.m))
	for k := range c.m {
		ks = append(ks, k)
	}
	return ks
}

// All snapshots the current dialers — the name checker asks each of them
// whether a bare name exists somewhere before minting a fake IP for it.
func (c *ClusterTransports) All() []Dialer {
	c.mu.Lock()
	defer c.mu.Unlock()
	ds := make([]Dialer, 0, len(c.m))
	for _, d := range c.m {
		ds = append(ds, d)
	}
	return ds
}

func (c *ClusterTransports) get(key string) (Dialer, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.m[key]
	return d, ok
}

// sole returns the only transport (and its key) when exactly one cluster is
// active, so the datapath can route transparently with no attribution; else ok=false.
func (c *ClusterTransports) sole() (Dialer, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) != 1 {
		return nil, "", false
	}
	for k, d := range c.m {
		return d, k, true
	}
	return nil, "", false
}

// StartGlobalDatapath brings up ONE datapath (TUN + routes + system DNS repoint +
// netstack) that routes each intercepted flow to the right cluster via
// multiDial(ct.get). The daemon fills ct with a per-cluster tunnel as clients of
// each cluster register. This is the macOS multicluster entry point; the
// single-cluster StartDatapath is unchanged.
func StartGlobalDatapath(ct *ClusterTransports, logf func(string, ...any)) (*Datapath, error) {
	return startDatapathDF(multiDial(ct), ct.All, logf)
}

// soleAllows decides whether a flow may take the single-cluster shortcut.
//
// The shortcut exists because with one cluster there is nothing to disambiguate,
// and it deliberately skips the ancestry walk so a detached child (setsid, a
// daemonised process) is not refused. But the datapath is MACHINE-WIDE: on macOS
// the daemon repoints the primary network service's resolver, so every process on
// the box, under any account, resolves a cluster name and gets a fake IP that
// connects. With two clusters up the walk already refuses what it cannot
// attribute. With one, "no ambiguity" had quietly become "no question asked", and
// a second local account reached another user's databases by typing their name.
//
// So one question, and only one: does this flow belong to an account that has a
// live client on this cluster. It REFUSES only when it has positively established
// that the answer is no. An unreadable socket table, a process that vanished
// between accept and lookup, a client too old to record its owner: all behave
// exactly as they did before this existed. A single-cluster session is plug's
// main path, and a datapath that starts refusing on a bad second would be worse
// than the leak it closes.
//
// It does not pretend to stop code running AS the user. That code can run plug
// itself, and no check here changes that.
func soleAllows(srcPort uint16, pidForConn func(uint16) (int, bool), uidOf func(int) (int, bool), owners map[int]bool) bool {
	if len(owners) == 0 {
		return true // nobody recorded an owner: unknown, which is not the same as nobody
	}
	pid, ok := pidForConn(srcPort)
	if !ok {
		return true
	}
	uid, ok := uidOf(pid)
	if !ok {
		return true
	}
	if uid == 0 {
		return true // root already owns the machine; refusing it buys nothing and can break the daemon's own probes
	}
	return owners[uid]
}
