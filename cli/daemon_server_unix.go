//go:build darwin || linux

package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/softwarity/plug/cli/internal/tunnel"
)

// runDaemon is the root helper (started by launchd/systemd). It serves one
// tunnel session at a time over the unix socket, mounting the TUN on request
// and tearing it down when the client disconnects.
func runDaemon() int {
	// Recover from a crash that may have left the system DNS pointed at a
	// now-dead tunnel — restore it before doing anything else.
	tunnel.RestoreLeftoverDNS()

	os.Remove(socketPath)
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		info("daemon: listen %s: %v", socketPath, err)
		return 1
	}
	defer ln.Close()
	// Local dev machine: let the user connect. (Documented trade-off.)
	os.Chmod(socketPath, 0o666)
	info("daemon: listening on %s", socketPath)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		tunnel.RestoreLeftoverDNS() // undo any in-flight DNS redirection
		ln.Close()
		os.Remove(socketPath)
		os.Exit(0)
	}()

	var mu sync.Mutex // one session at a time
	for {
		conn, err := ln.Accept()
		if err != nil {
			return 0
		}
		mu.Lock()
		handleSession(conn)
		mu.Unlock()
	}
}

func handleSession(conn net.Conn) {
	defer conn.Close()
	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)

	var req msg
	if err := dec.Decode(&req); err != nil {
		return
	}
	logf := func(format string, a ...any) { info("daemon: "+format, a...) }

	tr, err := tunnel.Dial(req.Host, req.Port, sshUser, embeddedKey)
	if err != nil {
		enc.Encode(msg{Error: err.Error()})
		return
	}
	defer tr.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	errc := make(chan error, 1)
	go func() { errc <- tunnel.Run(ctx, tr, req.Subnets, logf, ready) }()

	select {
	case <-ready:
	case err := <-errc:
		enc.Encode(msg{Error: err.Error()})
		return
	}
	if err := enc.Encode(msg{Ready: true}); err != nil {
		return // client vanished; deferred cancel tears the tunnel down
	}

	// Hold the tunnel up until the client says Done or disconnects (EOF).
	var end msg
	dec.Decode(&end)
	cancel()
	<-errc
	enc.Encode(msg{Closed: true})
}
