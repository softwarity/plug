//go:build darwin || linux

package main

import (
	"context"

	"github.com/softwarity/plug/cli/internal/tunnel"
)

// coreRunGo runs the experimental native Go tunnel (no sshuttle): an SSH
// transport to the agent + a userspace TUN, then the child command.
func coreRunGo(cfg config, subnets []string, cmdArgs []string) int {
	tr, err := tunnel.Dial(cfg.host, cfg.port, sshUser, embeddedKey)
	if err != nil {
		info("go-tunnel: %v", err)
		return 1
	}
	defer tr.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errc := make(chan error, 1)
	ready := make(chan struct{})
	go func() { errc <- tunnel.Run(ctx, tr, subnets, info, ready) }()

	select {
	case <-ready:
	case err := <-errc:
		info("go-tunnel: %v", err)
		return 1
	}

	code := runChild(cmdArgs)
	cancel()
	<-errc
	info("tunnel closed")
	return code
}
