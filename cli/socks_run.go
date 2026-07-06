package main

import (
	"net"
	"os"
	"path/filepath"

	"github.com/softwarity/plug/cli/internal/tunnel"
)

// dialTunnel opens the SSH transport to the cluster agent. Shared by coreRun
// (Linux/Windows) and the macOS datapath daemon. coreRun itself is per-OS
// (socks_run_darwin.go / socks_run_other.go): macOS routes through a persistent
// daemon, elsewhere each launch is autonomous.
func dialTunnel(cfg config) (*tunnel.Transport, error) {
	knownHosts := ""
	// TOFU host-key pinning is pointless for a loopback agent (there is no network
	// to intercept) and just causes false "host key changed" errors when a local
	// dev agent is recreated with a fresh key — so skip it for localhost. For real
	// hosts, pin the key next to the profiles.
	if !isLoopback(cfg.host) {
		if home, err := os.UserHomeDir(); err == nil {
			knownHosts = filepath.Join(home, ".plug", "known_hosts")
		}
	}
	tr, err := tunnel.Dial(cfg.host, cfg.port, sshUser, embeddedKey, knownHosts, info)
	// The host key is pinned into knownHosts on first connect. Off localhost the
	// dialer may be the setuid daemon (euid 0), which would leave the pin file
	// root-owned under the user's ~/.plug — and the "key changed, remove the line"
	// hint would then point at a file they can't edit without sudo. Hand it back.
	if err == nil && knownHosts != "" {
		chownToUser(knownHosts)
	}
	return tr, err
}

// isLoopback reports whether host is the local machine (no network to intercept).
func isLoopback(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
