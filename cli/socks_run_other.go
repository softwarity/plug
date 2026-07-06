//go:build !darwin

package main

import "github.com/softwarity/plug/cli/internal/tun"

// coreRun off macOS: each launch is autonomous — it dials its own tunnel and holds
// its own datapath for the child's lifetime. On Linux the child's resolver is
// scoped by its mount namespace, so concurrent launches never collide; no daemon
// is needed (restarting one process doesn't touch the others).
func coreRun(cfg config, cmdArgs []string) int {
	tr, err := dialTunnel(cfg)
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
