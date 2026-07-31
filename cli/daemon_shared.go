//go:build darwin || windows

package main

import (
	"net"
	"time"

	"github.com/softwarity/plug/cli/internal/tun"
	"github.com/softwarity/plug/cli/internal/tunnel"
)

// globalKey is the pseudo-cluster key under which the ONE global datapath (a detached
// daemon on macOS, an SCM service on Windows) holds the datapath for ALL clusters and
// routes each flow to the right tunnel by PID at connect (docs/multicluster.md). No
// real cluster key is ever "@global".
const globalKey = "@global"

// tunnelGrace keeps a cluster's tunnel open briefly after its last client goes away,
// so back-to-back `plug`s of that cluster reuse it (a ~0.2 s run) instead of
// re-dialing (~1 s). Idle timers live here; the datapath as a whole is still reaped
// after globalKey's longer grace once no cluster has any client at all.
const tunnelGrace = 20 * time.Second

var tunnelIdleSince = map[string]time.Time{}

// reconcileOnce opens a tunnel for each active cluster missing one and closes tunnels
// whose cluster no longer has a live client. Each open/close flips the cluster's ready
// marker so `plug -p X <cmd>` can wait for its own tunnel. Shared by macOS and Windows
// (both hold ONE datapath + a tunnel per active cluster; only the process model — a
// detached daemon vs an SCM service — differs).
func reconcileOnce(ct *tun.ClusterTransports, tunnels map[string]*tunnel.Transport) {
	active := map[string]bool{}
	for _, key := range tun.ActiveClusters() {
		active[key] = true
		if _, have := tunnels[key]; have {
			continue
		}
		host, port, err := net.SplitHostPort(key)
		if err != nil {
			host, port = key, ""
		}
		tr, err := dialTunnel(config{host: host, port: port})
		if err != nil {
			info("daemon: connect %s: %v", key, err)
			tun.MarkClusterError(key, err.Error()) // surface the reason to a waiting launcher
			continue
		}
		tunnels[key] = tr
		ct.Set(key, tr)
		tun.ClearClusterError(key)
		tun.MarkClusterReady(key)
		info("daemon: tunnel up for %s", key)
	}
	for key, tr := range tunnels {
		if active[key] {
			delete(tunnelIdleSince, key) // in use — reset the idle timer
			continue
		}
		// No live client. Hold the tunnel through tunnelGrace so back-to-back `plug`s
		// of the same cluster reuse it instead of paying a fresh dial each time.
		if tunnelIdleSince[key].IsZero() {
			tunnelIdleSince[key] = time.Now()
			continue
		}
		if time.Since(tunnelIdleSince[key]) < tunnelGrace {
			continue
		}
		tun.UnmarkClusterReady(key)
		ct.Remove(key)
		tr.Close()
		delete(tunnels, key)
		delete(tunnelIdleSince, key)
		info("daemon: tunnel down for %s", key)
	}
}

// reconcileLoop re-syncs the tunnel set with the active clusters. It polls often so a
// just-registered client's tunnel opens near-instantly (that open is what the launcher
// waits on); tunnelGrace, not the tick, governs how long an idle tunnel lives.
// reconcileLoop keeps the tunnel set matching the live clusters until stop is
// closed. It returns a channel closed when the loop has REALLY finished, which
// the teardown must wait on before touching `tunnels`.
//
// Closing stop is not enough: a tick already in flight can sit inside
// dialTunnel for the whole dial timeout, and it writes to the map on the way
// out. Tearing down concurrently was a plain data race — "concurrent map
// iteration and map write" kills the process, and this one runs as root holding
// the machine's DNS, so it died before restoring the resolver. The race
// detector, now on in CI, is what keeps this honest.
func reconcileLoop(ct *tun.ClusterTransports, tunnels map[string]*tunnel.Transport, stop <-chan struct{}) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		tk := time.NewTicker(300 * time.Millisecond)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				reconcileOnce(ct, tunnels)
			}
		}
	}()
	return done
}

// reapGlobal stops the datapath once no cluster has had a live client for grace —
// long enough to ride through a kill+relaunch of a process.
func reapGlobal(dp *tun.Datapath, stop <-chan struct{}) {
	const grace = 30 * time.Second
	tk := time.NewTicker(2 * time.Second)
	defer tk.Stop()
	var emptySince time.Time
	for {
		select {
		case <-stop:
			return
		case <-tk.C:
			if len(tun.ActiveClusters()) > 0 {
				emptySince = time.Time{}
				continue
			}
			if emptySince.IsZero() {
				emptySince = time.Now()
				continue
			}
			if time.Since(emptySince) >= grace {
				dp.Stop()
				return
			}
		}
	}
}

// closeAll drops every tunnel. The caller MUST have waited for reconcileLoop's
// done channel first — nothing else guards this map.
func closeAll(ct *tun.ClusterTransports, tunnels map[string]*tunnel.Transport) {
	for key, tr := range tunnels {
		tun.UnmarkClusterReady(key)
		ct.Remove(key)
		tr.Close()
		delete(tunnels, key)
	}
}
