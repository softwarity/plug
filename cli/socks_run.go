package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	// Names provisioned so far — dropped on teardown AND on any error below (a
	// signpost/Service created for spec N must not survive a failure on N or N+1).
	var dynamic []string
	drop := func() {
		for _, name := range dynamic {
			_, _ = tr.Exec("unserve-name " + name)
		}
	}
	fail := func(err error) (func(), error) {
		drop()
		tr.Close()
		return nil, err
	}
	for _, spec := range cfg.exposes {
		ex, err := tr.Expose(spec)
		if err != nil {
			return fail(err)
		}
		// Ask the agent to provision the NAME dynamically (a docker signpost, a
		// k8s Service — whatever the deployment opted into). "static" means no
		// dynamic backend (or an agent from before the verb): the pre-declared
		// alias must carry the name, and Verify will tell.
		mode, err := tr.Exec("serve-name " + spec.Name + " " + spec.ClusterPort)
		if err != nil {
			return fail(err)
		}
		if strings.HasPrefix(mode, "error:") {
			return fail(fmt.Errorf("%s: agent: %s", spec.Name, strings.TrimSpace(strings.TrimPrefix(mode, "error:"))))
		}
		if mode != "dynamic" {
			mode = "static"
		} else {
			// Provisioned — register for cleanup BEFORE Verify, so a Verify
			// failure below still tears the name down.
			dynamic = append(dynamic, spec.Name)
		}
		// -s was asked for explicitly: an unproven path fails the session, with
		// the remedy — never a silent no-op (fix the cluster side, run again).
		// A dynamic name needs a beat to exist (signpost start, DNS): retry the
		// check a few times before giving up.
		verr := ex.Verify()
		for i := 0; verr != nil && mode == "dynamic" && i < 3; i++ {
			time.Sleep(1500 * time.Millisecond)
			verr = ex.Verify()
		}
		if verr != nil {
			if mode == "static" {
				// The agent has no dynamic backend HERE (no socket/RBAC, or it couldn't
				// provision — e.g. a Swarm agent not on a manager node), so it expected
				// a pre-declared name and found none. Name both fixes — the
				// auto-provisioning one is what most people actually want.
				return fail(fmt.Errorf("%w\n"+
					"      the agent answered STATIC (no auto-provisioning here). Either:\n"+
					"      • for auto-provisioning (no name to declare): mount the Docker socket on the agent\n"+
					"        (on Swarm, run it as a service on a manager node); on Kubernetes, apply the RBAC;\n"+
					"      • or pre-declare %q yourself (a network alias on the plug service, or a k8s Service)",
					verr, spec.Name))
			}
			return fail(verr)
		}
		info("serving %s (%s name, path verified through the cluster)", spec, mode)
		if mode == "dynamic" {
			// After a reconnect, a restarted agent has GC'd the signpost — re-run
			// serve-name and re-verify, so the name isn't silently dead while the
			// forward reports re-armed. (Static names are pre-declared; no hook.)
			ex.OnRearm(func() error {
				m, err := tr.Exec("serve-name " + spec.Name + " " + spec.ClusterPort)
				if err != nil {
					return err
				}
				if strings.HasPrefix(m, "error:") {
					return fmt.Errorf("agent: %s", strings.TrimSpace(strings.TrimPrefix(m, "error:")))
				}
				return ex.Verify()
			})
		}
	}
	return func() {
		// Drop the dynamic names BEFORE the transport goes: a signpost/Service
		// must not outlive its session.
		drop()
		tr.Close()
	}, nil
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
