package main

import (
	"os"
	"path/filepath"

	"github.com/softwarity/plug/cli/internal/tunnel"
)

// dialTunnel opens the SSH transport to the cluster agent (TOFU host-key pin next
// to the profiles). Shared by coreRun (Linux/Windows) and the macOS datapath
// daemon. coreRun itself is per-OS (socks_run_darwin.go / socks_run_other.go):
// macOS routes through a persistent daemon, elsewhere each launch is autonomous.
func dialTunnel(cfg config) (*tunnel.Transport, error) {
	knownHosts := ""
	if home, err := os.UserHomeDir(); err == nil {
		knownHosts = filepath.Join(home, ".plug", "known_hosts")
	}
	return tunnel.Dial(cfg.host, cfg.port, sshUser, embeddedKey, knownHosts, info)
}
