//go:build darwin

package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/softwarity/plug/cli/internal/tun"
)

// doctorOS — the macOS-side checks: the setuid grant, the per-cluster daemon
// (alive? which CORE binary does it actually run — the one thing the
// per-cluster version mechanism does not refresh), the system resolver state
// (a stale plug override with no session is THE dirty state), Docker Desktop's
// belt-and-braces DNS, and a live honest-NXDOMAIN probe through a running
// datapath.
func doctorOS(add func(check)) {
	// Privilege: the INSTALLED launcher is setuid root (granted once at
	// install). Check that one — running doctor from a dev build must not
	// cry wolf about the build's own file mode.
	target, _ := os.Executable()
	if home, err := os.UserHomeDir(); err == nil {
		if inst := home + "/.local/bin/plug"; fileExists(inst) {
			target = inst
		}
	}
	if fi, err := os.Stat(target); err == nil {
		if fi.Mode()&os.ModeSetuid != 0 {
			add(check{area: "local", name: "privilege", status: stOK,
				detail: "setuid grant in place (" + target + ")"})
		} else {
			add(check{area: "local", name: "privilege", status: stWarn,
				detail: target + " is not setuid root",
				remedy: "re-run the install one-liner (it grants the privilege once)"})
		}
	}

	// Per-profile daemon: alive, and WHICH core binary it runs.
	daemons := 0
	for _, p := range listProfiles() {
		host, port, err := readProfileSoft(p)
		if err != nil {
			continue
		}
		key := host + ":" + port
		if !tun.DaemonAlive(key) {
			continue
		}
		daemons++
		pid := tun.DaemonPID(key)
		bin := ""
		if out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output(); err == nil {
			bin = strings.TrimSpace(string(out))
		}
		detail := fmt.Sprintf("running (pid %d)", pid)
		st, remedy := stOK, ""
		if v := versionFromCorePath(bin); v != "" {
			detail += ", core v" + v
		} else if bin != "" {
			// Not a cached versioned core — likely the launcher itself (dev) or a
			// stale path.
			detail += ", " + bin
		}
		add(check{area: "local", name: "daemon (" + p + ")", status: st, detail: detail, remedy: remedy})
	}

	// System resolver: pointed at plug? Legitimate while sessions live; STALE
	// (the daemon crashed / teardown missed) when nothing runs — the state that
	// broke machine-wide DNS once.
	out, _ := exec.Command("scutil", "--dns").Output()
	plugged := strings.Contains(string(out), "198.18.")
	sessions := 0
	for _, k := range tun.ActiveClusters() {
		sessions += tun.LiveClients(k)
	}
	switch {
	case plugged && sessions == 0 && daemons == 0:
		add(check{area: "local", name: "system resolver", status: stFail,
			detail: "still pointed at plug with NO live session (stale override)",
			remedy: "plug down (restores the resolver), then re-check"})
	case plugged:
		add(check{area: "local", name: "system resolver", status: stOK,
			detail: fmt.Sprintf("plugged (%d live client(s)) — normal while sessions run", sessions)})
	default:
		add(check{area: "local", name: "system resolver", status: stOK, detail: "untouched"})
	}

	// Docker Desktop on this machine: with explicit upstream DNS, unknown names
	// never even leave the VM (belt and braces on top of honest NXDOMAIN).
	if home, err := os.UserHomeDir(); err == nil {
		if data, err := os.ReadFile(home + "/.docker/daemon.json"); err == nil {
			if strings.Contains(string(data), `"dns"`) {
				add(check{area: "local", name: "docker desktop dns", status: stOK,
					detail: `daemon.json pins upstream "dns" (belt and braces)`})
			} else {
				add(check{area: "local", name: "docker desktop dns", status: stOK,
					detail: `no "dns" pin — fine since 2.2 (honest NXDOMAIN); pin one for belt and braces`})
			}
		}
	}

	// Honest-NXDOMAIN, live: with a datapath running, an absent bare name must
	// FAIL to resolve. A minted 198.18 answer means the RUNNING datapath
	// predates 2.2 — the exact service-vs-launcher version gap, caught red-handed.
	if plugged && sessions > 0 {
		addrs, err := net.LookupHost("plug-doctor-absent-probe")
		if err == nil && len(addrs) > 0 && strings.HasPrefix(addrs[0], "198.18.") {
			add(check{area: "local", name: "honest NXDOMAIN", status: stWarn,
				detail: "an absent name minted a fake IP — the RUNNING datapath predates 2.2",
				remedy: "end the sessions (or plug down), relaunch — the new core takes over"})
		} else if err != nil {
			add(check{area: "local", name: "honest NXDOMAIN", status: stOK,
				detail: "absent names answer NXDOMAIN through the live datapath"})
		}
	}
}

// doctorSessions reports the registry view: which clusters have live clients.
func doctorSessions(add func(check)) {
	keys := tun.ActiveClusters()
	if len(keys) == 0 {
		add(check{area: "local", name: "sessions", status: stOK, detail: "none running"})
		return
	}
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s (%d)", k, tun.LiveClients(k)))
	}
	add(check{area: "local", name: "sessions", status: stOK, detail: strings.Join(parts, " · ")})
}

// versionFromCorePath extracts "x.y.z" from a cached-core path
// (…/.plug/versions/<v>/plug), or "" when the path is not one.
func versionFromCorePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		if s == "versions" && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	return ""
}
