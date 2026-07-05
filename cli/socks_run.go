package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/softwarity/plug/cli/internal/inject"
	"github.com/softwarity/plug/cli/internal/tunnel"
)

// coreRunSOCKS runs the zero-root data path: a local SOCKS5 proxy AND a local
// HTTP proxy backed by the SSH transport, plus a local port-forward per declared
// raw-TCP service, with the child command's environment pointed at all of it.
// Cluster names resolve because both proxies hand the hostname to sshd (which
// resolves it inside the cluster). No root, no TUN — several sessions to
// different clusters coexist.
//
// Two proxies because clients split on which they understand: curl, the JVM and
// SOCKS-native tools take the SOCKS one; Node's HTTP stack (axios/fetch) speaks
// HTTP-proxy only and chokes on SOCKS, so it gets the HTTP one.
func coreRunSOCKS(cfg config, cmdArgs []string) int {
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// SOCKS5 proxy — curl, the JVM (via -DsocksProxyHost), Go, SOCKS-native clients.
	socksAddr, err := startProxy(func(ready chan<- string) error {
		return tr.ServeSOCKS(ctx, "127.0.0.1:0", info, ready)
	})
	if err != nil {
		info("socks: %v", err)
		return 1
	}

	// HTTP proxy — Node's HTTP stack (axios/follow-redirects/fetch) understands
	// an HTTP proxy but not a SOCKS one, so HTTP_PROXY/HTTPS_PROXY point here.
	httpAddr, err := startProxy(func(ready chan<- string) error {
		return tr.ServeHTTPProxy(ctx, "127.0.0.1:0", info, ready)
	})
	if err != nil {
		info("http proxy: %v", err)
		return 1
	}

	env := proxyEnv(socksAddr, httpAddr)

	// N1: transparent connect()/DNS interception. On top of the proxy env, inject
	// a native hook into the child so libc-based runtimes (Node, JVM, Python,
	// curl…) route EVERY outbound TCP connection and DNS lookup through the SOCKS
	// proxy above — cluster-side resolution, no per-service forward. This is
	// additive; the proxy env AND the forwards below still apply, and anything the
	// hook can't reach (Go/static binaries, non-TCP, SIP'd system binaries) falls
	// back to them. Disable with PLUG_NO_INJECT=1; a no-op where unavailable.
	if extra := inject.Env(socksAddr, info); extra != nil {
		env = append(env, extra...)
	}

	// Per-session port-forwards for raw-TCP drivers that ignore both proxies.
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
	info("tunnel closed")
	return code
}

// startProxy launches serve in a goroutine and returns its bound local address
// once it signals ready (or the startup error). The server runs until its ctx is
// cancelled, after which its listener closes and the goroutine exits.
func startProxy(serve func(ready chan<- string) error) (string, error) {
	ready := make(chan string, 1)
	errc := make(chan error, 1)
	go func() { errc <- serve(ready) }()
	select {
	case addr := <-ready:
		return addr, nil
	case err := <-errc:
		return "", err
	}
}

// proxyEnv returns the environment pre-wired to the two proxies:
//   - HTTP_PROXY/HTTPS_PROXY → the HTTP proxy (Node's axios/fetch, curl, Go…),
//   - ALL_PROXY → the SOCKS proxy (SOCKS-native tools, non-HTTP TCP),
//   - JAVA_TOOL_OPTIONS → -DsocksProxyHost (the whole JVM: HTTP, JDBC, AMQP).
//
// For an http(s) target HTTP_PROXY wins over ALL_PROXY everywhere it matters, so
// HTTP clients never see the SOCKS URL they can't parse.
func proxyEnv(socksAddr, httpAddr string) []string {
	host, port, _ := strings.Cut(socksAddr, ":")
	socks := "socks5h://" + socksAddr
	httpProxy := "http://" + httpAddr

	env := os.Environ()
	set := func(k, v string) { env = append(env, k+"="+v) }
	set("HTTP_PROXY", httpProxy)
	set("HTTPS_PROXY", httpProxy)
	set("http_proxy", httpProxy)
	set("https_proxy", httpProxy)
	set("ALL_PROXY", socks)
	set("all_proxy", socks)
	// Local port-forwards must NOT go back through a proxy, or the proxy would
	// try to reach the local port inside the cluster.
	set("NO_PROXY", "127.0.0.1,localhost,::1")
	set("no_proxy", "127.0.0.1,localhost,::1")
	jopts := fmt.Sprintf("-DsocksProxyHost=%s -DsocksProxyPort=%s", host, port)
	if existing := os.Getenv("JAVA_TOOL_OPTIONS"); existing != "" {
		jopts = existing + " " + jopts
	}
	set("JAVA_TOOL_OPTIONS", jopts)
	return env
}
