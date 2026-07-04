package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/softwarity/plug/cli/internal/tunnel"
)

// coreRunSOCKS runs the zero-root data path: a local SOCKS5 proxy backed by the
// SSH transport, plus a local port-forward per declared raw-TCP service, with
// the child command's environment pointed at all of it. Cluster names resolve
// because socks5h hands the hostname to sshd (which resolves it inside the
// cluster). No root, no TUN — several sessions to different clusters coexist.
func coreRunSOCKS(cfg config, cmdArgs []string) int {
	tr, err := tunnel.Dial(cfg.host, cfg.port, sshUser, embeddedKey)
	if err != nil {
		info("connect: %v", err)
		return 1
	}
	defer tr.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ready := make(chan string, 1)
	errc := make(chan error, 1)
	go func() { errc <- tr.ServeSOCKS(ctx, "127.0.0.1:0", info, ready) }()

	var socksAddr string
	select {
	case socksAddr = <-ready:
	case err := <-errc:
		info("socks: %v", err)
		return 1
	}

	env := proxyEnv(socksAddr)

	// Per-session port-forwards for raw-TCP drivers that ignore the proxy.
	for _, f := range cfg.forwards {
		local, err := tr.Forward(ctx, "127.0.0.1:0", f.target, info)
		if err != nil {
			info("forward %s: %v", f.target, err)
			continue
		}
		env = append(env, f.env+"="+f.localValue(local))
		info("forward %s → %s (%s)", f.target, local, f.env)
	}

	info("tunnel ready — running your command")
	code := runChildEnv(cmdArgs, env)
	cancel()
	<-errc
	info("tunnel closed")
	return code
}

// proxyEnv returns the environment pre-wired to the SOCKS proxy for the common
// runtimes: ALL_PROXY (curl, Go, many libs) and JAVA_TOOL_OPTIONS (the whole
// JVM — Spring/Quarkus HTTP, JDBC, AMQP).
func proxyEnv(socksAddr string) []string {
	host, port, _ := strings.Cut(socksAddr, ":")
	socks := "socks5h://" + socksAddr

	env := os.Environ()
	set := func(k, v string) { env = append(env, k+"="+v) }
	set("ALL_PROXY", socks)
	set("all_proxy", socks)
	set("HTTP_PROXY", socks)
	set("HTTPS_PROXY", socks)
	set("http_proxy", socks)
	set("https_proxy", socks)
	// Local port-forwards must NOT go back through the proxy, or the proxy
	// would try to reach the local port inside the cluster.
	set("NO_PROXY", "127.0.0.1,localhost,::1")
	set("no_proxy", "127.0.0.1,localhost,::1")
	jopts := fmt.Sprintf("-DsocksProxyHost=%s -DsocksProxyPort=%s", host, port)
	if existing := os.Getenv("JAVA_TOOL_OPTIONS"); existing != "" {
		jopts = existing + " " + jopts
	}
	set("JAVA_TOOL_OPTIONS", jopts)
	return env
}
