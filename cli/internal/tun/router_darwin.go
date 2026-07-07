//go:build darwin

package tun

// multiDial builds the multicluster dialFunc for the global daemon: attribute an
// intercepted flow to a cluster — the app's source port → the PID that owns the
// socket (lsof) → walk its ancestry (ps) → the registered `plug -p X` launcher
// (clusterForPID) — then hand back that cluster's live transport. It refuses
// (ok=false → RST) a flow it can't attribute, e.g. a process detached via setsid.
// `transport` is the daemon's live tunnel set (key → Dialer).
func multiDial(transport func(key string) (Dialer, bool)) dialFunc {
	r := pidRouter{
		clusterForPID: clusterForPID,
		pidForConn:    pidForLocalPort,
		ppidOf:        ppidOf,
	}
	return func(srcPort uint16) (Dialer, bool) {
		key, ok := r.route(srcPort)
		if !ok {
			return nil, false
		}
		return transport(key)
	}
}
