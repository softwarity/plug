//go:build darwin

package main

import (
	"os"
	"time"

	"github.com/softwarity/plug/cli/internal/tun"
)

// coreRun on macOS: the datapath is held by ONE persistent GLOBAL daemon that
// serves every cluster (macOS repoints DNS machine-wide, so a per-cluster daemon
// can't coexist). A `plug -p X <cmd>` opens NO tunnel of its own — it registers as
// a client of cluster X (the marker carries X's key), makes sure the daemon is up,
// waits for the daemon to have opened X's tunnel, then runs the child. The child's
// flows are attributed back to X by PID at connect. Restart your processes freely:
// the daemon (and every tunnel) survives them.
func coreRun(cfg config, cmdArgs []string) int {
	key := cfg.host + ":" + cfg.port
	// Register FIRST (the marker carries our cluster key) so the daemon's reconcile
	// always sees us — and opens our cluster's tunnel — before we run.
	unregister := tun.RegisterClient(key, os.Getpid())
	defer unregister()

	if !tun.DaemonAlive(globalKey) {
		// Two `plug` launched at once both try to start the daemon; only one wins
		// the leader flock. If OUR attempt fails but a daemon is now up (the other
		// won), that's fine — proceed to wait for our cluster's tunnel.
		if err := startDaemonDetached(cfg); err != nil && !tun.DaemonAlive(globalKey) {
			info("cannot start the plug daemon: %v", err)
			return 1
		}
	}
	waitClusterReady(key)
	return runChildEnv(cmdArgs, nil)
}

// waitClusterReady blocks until the daemon has opened OUR cluster's tunnel (its
// ready marker) or a short timeout — then runs the child anyway (best effort: the
// datapath is up daemon-wide, the tunnel opens on the daemon's next reconcile).
func waitClusterReady(key string) {
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if tun.ClusterReady(key) {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	info("cluster %s: tunnel not ready yet — starting anyway", key)
}
