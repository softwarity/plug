package main

import (
	"os"
	"path/filepath"

	"github.com/softwarity/plug/cli/internal/tun"
	"github.com/softwarity/plug/cli/internal/tunnel"
)

// coreRun is plug's single data path: dial the SSH transport to the agent, then
// run the child under the userspace TUN. The TUN captures the child's cluster
// traffic at the IP layer and forwards it through the tunnel BY NAME, so every
// runtime is covered uniformly (libc, Go/static, gRPC/Netty/grpcio, …) on
// Linux/macOS/Windows. It needs root to create the device + routes; the cluster
// install script sets that up once (a root helper), so day-to-day runs are
// plain `plug <cmd>`.
func coreRun(cfg config, cmdArgs []string) int {
	// TOFU host-key pin lives next to the profiles, keyed by host:port.
	knownHosts := ""
	if home, err := os.UserHomeDir(); err == nil {
		knownHosts = filepath.Join(home, ".plug", "known_hosts")
	}
	tr, err := tunnel.Dial(cfg.host, cfg.port, sshUser, embeddedKey, knownHosts, info)
	if err != nil {
		info("connect: %v", err)
		return 1
	}
	defer tr.Close()

	info("tunnel ready — running your command")
	code, rerr := tun.Run(tr, cmdArgs, info)
	if rerr != nil {
		info("%v", rerr)
	}
	return code
}
