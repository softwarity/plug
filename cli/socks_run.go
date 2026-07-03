package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/softwarity/plug/cli/internal/tunnel"
)

// coreRunSOCKS runs the zero-root data path: a local SOCKS5 proxy backed by the
// SSH transport, with the child command's environment pointed at it. Cluster
// names resolve because socks5h hands the hostname to sshd (which resolves it
// inside the cluster).
func coreRunSOCKS(cfg config, cmdArgs []string) int {
	tr, err := tunnel.Dial(cfg.host, cfg.port, sshUser, embeddedKey)
	if err != nil {
		info("socks: %v", err)
		return 1
	}
	defer tr.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ready := make(chan string, 1)
	errc := make(chan error, 1)
	go func() { errc <- tr.ServeSOCKS(ctx, "127.0.0.1:0", info, ready) }()

	var addr string
	select {
	case addr = <-ready:
	case err := <-errc:
		info("socks: %v", err)
		return 1
	}

	info("cluster reachable via the proxy; child env points at it")
	code := runChildWithProxy(cmdArgs, addr)
	cancel()
	<-errc
	info("proxy closed")
	return code
}

// runChildWithProxy runs the command with the proxy pre-wired for the common
// runtimes: ALL_PROXY (curl, Go, many libs) and JAVA_TOOL_OPTIONS (the whole
// JVM — Spring/Quarkus HTTP, JDBC, AMQP).
func runChildWithProxy(cmdArgs []string, addr string) int {
	host, port, _ := strings.Cut(addr, ":")
	socks := "socks5h://" + addr

	env := os.Environ()
	set := func(k, v string) { env = append(env, k+"="+v) }
	set("ALL_PROXY", socks)
	set("all_proxy", socks)
	set("HTTP_PROXY", socks)
	set("HTTPS_PROXY", socks)
	set("http_proxy", socks)
	set("https_proxy", socks)
	jopts := fmt.Sprintf("-DsocksProxyHost=%s -DsocksProxyPort=%s", host, port)
	if existing := os.Getenv("JAVA_TOOL_OPTIONS"); existing != "" {
		jopts = existing + " " + jopts
	}
	set("JAVA_TOOL_OPTIONS", jopts)

	return runChildEnv(cmdArgs, env)
}
