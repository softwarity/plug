//go:build darwin

package main

import (
	"os"

	"github.com/softwarity/plug/cli/internal/tun"
)

// coreRun on macOS: the datapath is held by a persistent per-cluster DAEMON, so a
// `plug <cmd>` opens NO tunnel and holds NO datapath of its own — it just makes
// sure the daemon is up, registers as a live client, and runs the child (which
// resolves cluster names through the daemon's machine-wide DNS). This is what lets
// you restart your processes freely: the daemon (and the datapath) survive them.
func coreRun(cfg config, cmdArgs []string) int {
	key := cfg.host + ":" + cfg.port
	// Register FIRST, so the daemon's reaper always counts us before we probe it —
	// this closes the race where it could tear down between our check and our child.
	unregister := tun.RegisterClient(key, os.Getpid())
	defer unregister()

	if !tun.DaemonAlive(key) {
		if err := startDaemonDetached(cfg); err != nil {
			info("cannot start the plug daemon: %v", err)
			return 1
		}
	}
	return runChildEnv(cmdArgs, nil)
}
