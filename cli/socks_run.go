package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/softwarity/plug/cli/internal/inject"
	"github.com/softwarity/plug/cli/internal/seccomp"
	"github.com/softwarity/plug/cli/internal/tun"
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

	// Root mode: a userspace TUN data path (needs root). It captures the child's
	// cluster traffic at the IP layer and forwards it through the tunnel, so EVERY
	// runtime is covered — including gRPC/Netty/grpcio, which the fd-level hook +
	// supervisor can't do. One codebase for Linux/macOS/Windows. Bypasses the
	// hook/supervisor/env-proxy entirely.
	if cfg.root {
		info("tunnel ready — running your command (TUN mode)")
		code, rerr := tun.Run(tr, cmdArgs, info)
		if rerr != nil {
			info("%v", rerr)
		}
		return code
	}

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

	// N1: transparent connect()/DNS interception. Load a native hook into the
	// child so libc runtimes (Node, JVM, Python, curl…) route EVERY outbound TCP
	// connection and DNS lookup through the SOCKS proxy — cluster-side resolution,
	// no per-service forward. On Linux this is paired with a rootless seccomp
	// supervisor that extends the SAME coverage to Go / statically-linked binaries
	// (which bypass libc for both resolution and connect). Disable with
	// PLUG_NO_INJECT=1 / PLUG_NO_SECCOMP=1.
	cmdArgs, inj := setupInjection(cmdArgs, socksAddr)

	var env []string
	if inj != nil && runtime.GOOS == "linux" {
		// Linux: the hook + the seccomp supervisor intercept every connect()
		// transparently, so the app-level proxy env is redundant here — and it
		// actively interferes with some JVM runtimes: -DsocksProxyHost makes the
		// JVM hand the (hook-minted) fake IP to the proxy, which can't route it.
		// Rely on the hook/supervisor; only pin loopback direct.
		env = append(os.Environ(),
			"NO_PROXY=127.0.0.1,localhost,::1", "no_proxy=127.0.0.1,localhost,::1")
		env = append(env, inj...)
	} else {
		// macOS (DYLD hook, no supervisor) or no injection at all: keep the
		// env-proxy as a fallback next to whatever injection is available (inj is
		// nil when the hook is unavailable here).
		env = proxyEnv(socksAddr, httpAddr)
		env = append(env, inj...)
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

// setupInjection loads the transparent hook and, on Linux, wraps the command
// with the seccomp supervisor that extends coverage to Go / statically-linked
// binaries. It returns the (possibly wrapped) command and the extra environment.
//
// macOS / Linux-without-supervisor: the command is unchanged and the env carries
// the loader var (DYLD_INSERT_LIBRARIES / LD_PRELOAD) + PLUG_SOCKS, as before.
//
// Linux-with-supervisor: the command becomes `plug-seccomp <cmd>…` and the env
// carries PLUG_SOCKS plus PLUG_PRELOAD — the hook the supervisor re-injects into
// the CHILD only. We deliberately do NOT set LD_PRELOAD on the supervisor itself,
// so its own resolver/SOCKS calls stay real (unhooked); it sets LD_PRELOAD for
// the app right before exec.
func setupInjection(cmdArgs []string, socksAddr string) ([]string, []string) {
	lib, ok := inject.Prepare(info)
	if !ok {
		return cmdArgs, nil
	}
	preload := inject.AppendPreload(os.Getenv(inject.PreloadVar()), lib)

	if runtime.GOOS == "linux" && seccomp.Available() {
		if sup, ok := seccomp.Prepare(info); ok {
			info("injection on — %s + seccomp Go-coverage supervisor", filepath.Base(lib))
			return append([]string{sup}, cmdArgs...), []string{
				seccomp.EnvVarPreload + "=" + preload,
				inject.EnvVarSocks + "=" + socksAddr,
			}
		}
	}

	info("injection on — %s (transparent connect/DNS via SOCKS)", filepath.Base(lib))
	return cmdArgs, []string{
		inject.PreloadVar() + "=" + preload,
		inject.EnvVarSocks + "=" + socksAddr,
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
