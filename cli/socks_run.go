package main

import (
	"net"
	"os"
	"path/filepath"

	"github.com/softwarity/plug/cli/internal/tun"
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
		if shared := tun.SharedKnownHosts(); shared != "" {
			// Windows: a machine-wide, user-writable path (%ProgramData%\plug) shared by
			// the SYSTEM service and the launcher. The service can't pin under the user's
			// home, and its own profile dir isn't user-accessible — so a "host key changed"
			// there could not be reset without admin. Here the user can remove the line.
			knownHosts = shared
		} else if home, err := os.UserHomeDir(); err == nil {
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

// startExposes arms the session's -s mappings (the reverse direction) on a
// DEDICATED transport, so the listeners' lifetime is exactly this session's —
// even on macOS/Windows where the forward datapath lives in a shared daemon.
// Each mapping is verified end-to-end once (through the cluster's own DNS), so
// a missing alias, a too-old agent image, or a competing instance fails loud at
// startup instead of silently never receiving traffic. Returns the transport
// teardown (a no-op when nothing is exposed).
func startExposes(cfg config) (func(), error) {
	if len(cfg.exposes) == 0 {
		return func() {}, nil
	}
	tr, err := dialTunnel(cfg)
	if err != nil {
		return nil, err
	}
	for _, spec := range cfg.exposes {
		ex, err := tr.Expose(spec)
		if err != nil {
			tr.Close()
			return nil, err
		}
		// -s was asked for explicitly: an unproven path fails the session, with
		// the remedy — never a silent no-op (fix the cluster side, run again).
		if verr := ex.Verify(); verr != nil {
			tr.Close()
			return nil, verr
		}
		info("serving %s (path verified through the cluster)", spec)
	}
	return func() { tr.Close() }, nil
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
