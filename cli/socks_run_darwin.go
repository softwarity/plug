//go:build darwin

package main

import (
	"os"
	"strings"

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
	// One cluster, one account. Registering is what makes a process a member of a
	// cluster, and it happens before anything authenticates, so this is the moment
	// to refuse: see tun.ClusterHeldByOther.
	if other, held := tun.ClusterHeldByOther(key, tun.ThisAccount()); held {
		info(tun.ClusterHeldRefusal, key, other)
		return 1
	}
	// Register FIRST (the marker carries our cluster key) so the daemon's reconcile
	// always sees us — and opens our cluster's tunnel — before we run.
	unregister := tun.RegisterClient(key, os.Getpid(), cfg.key)
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
	// The reverse direction is per-session, not per-cluster: it rides its own
	// transport in THIS process, not the shared daemon — Ctrl-C closes the port.
	stopExposes, err := startExposes(cfg)
	if err != nil {
		info("expose: %v", err)
		return 1
	}
	defer stopExposes()
	return runChildEnv(cmdArgs, goResolverEnv())
}

// goResolverEnv is the child environment with GODEBUG=netdns=go appended. macOS
// only: on a network without a usable default resolver, getaddrinfo stalls a
// flat ~5s per single-label lookup (mDNSResponder tries .local before the search
// domain — measured 5.02s while plug's own DNS answered in 20ms), which killed
// every Go client with a 5s timeout. Go's PURE resolver reads the
// /etc/resolv.conf plug writes and answered in ~0.2s in the same session — so
// put Go children on that proven path. Non-Go children ignore GODEBUG entirely;
// an explicit user netdns choice is preserved.
func goResolverEnv() []string {
	env := os.Environ()
	for i, kv := range env {
		if v, ok := strings.CutPrefix(kv, "GODEBUG="); ok {
			if strings.Contains(v, "netdns=") {
				return env // the user already chose a resolver — keep it
			}
			env[i] = kv + ",netdns=go"
			return env
		}
	}
	return append(env, "GODEBUG=netdns=go")
}
