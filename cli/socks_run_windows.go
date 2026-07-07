//go:build windows

package main

import (
	"os"
	"time"

	"github.com/softwarity/plug/cli/internal/tun"
)

// coreRun on Windows: the datapath is held by the SYSTEM service (daemon_windows.go),
// which serves every cluster. A `plug -p X <cmd>` opens NO tunnel of its own and needs
// NO admin — it registers as a client of cluster X (the marker carries X's key), makes
// sure the service is running (starting it through the SCM, which the installer lets
// users do), waits for the service to have opened X's tunnel, then runs the child. The
// child's flows are attributed back to X by PID at connect. Restart processes freely:
// the service and every tunnel survive them.
func coreRun(cfg config, cmdArgs []string) int {
	key := cfg.host + ":" + cfg.port
	// Register FIRST (marker carries our cluster key) so the service's reconcile sees
	// us — and opens our cluster's tunnel — before we run.
	unregister := tun.RegisterClient(key, os.Getpid())
	defer unregister()

	if !tun.DaemonAlive(globalKey) {
		// Two `plug` at once both try to start the service; the SCM serialises it.
		// If OUR start fails but the service is now up (the other won), proceed.
		if err := startService(); err != nil && !tun.DaemonAlive(globalKey) {
			info("cannot start the plug service: %v", err)
			info("is the service installed? re-run the installer once (it needs admin only to install the service).")
			return 1
		}
	}
	waitClusterReady(key)
	return runChildEnv(cmdArgs, nil)
}

// waitClusterReady blocks until the service has opened OUR cluster's tunnel (its ready
// marker) or a short timeout — then runs the child anyway (best effort: the datapath
// is up service-wide, the tunnel opens on the service's next reconcile).
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
