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
			continue
		}
		tunnels[key] = tr
		ct.Set(key, tr)
		tun.MarkClusterReady(key)
		info("daemon: tunnel up for %s", key)
	}
	for key, tr := range tunnels {
		if !active[key] {
			tun.UnmarkClusterReady(key)
			ct.Remove(key)
			tr.Close()
			delete(tunnels, key)
			info("daemon: tunnel down for %s", key)
		}
	}
}

// reconcileLoop re-syncs the tunnel set with the active clusters every 2s.
func reconcileLoop(ct *tun.ClusterTransports, tunnels map[string]*tunnel.Transport, stop <-chan struct{}) {
	tk := time.NewTicker(2 * time.Second)
	defer tk.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tk.C:
			reconcileOnce(ct, tunnels)
		}
	}
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

func closeAll(ct *tun.ClusterTransports, tunnels map[string]*tunnel.Transport) {
	for key, tr := range tunnels {
		tun.UnmarkClusterReady(key)
		ct.Remove(key)
		tr.Close()
		delete(tunnels, key)
	}
}
