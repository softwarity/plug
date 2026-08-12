//go:build linux

package main

import (
	"os"
	"os/exec"
	"strings"

	"github.com/softwarity/plug/cli/internal/tun"
)

// doctorOS — the Linux-side checks. Linux needs far less: the datapath is
// per-launch (private mount namespace, private resolv.conf), so there is no
// global daemon, no machine-wide resolver to go stale, and no service binary
// to drift. What can break is the one-time privilege grant on the launcher.
func doctorOS(add func(check)) {
	self, err := os.Executable()
	if err != nil {
		return
	}
	if out, err := exec.Command("getcap", self).Output(); err == nil {
		if strings.Contains(string(out), "cap_net_admin") {
			add(check{area: "local", name: "privilege", status: stOK, detail: "file capabilities in place"})
		} else {
			add(check{area: "local", name: "privilege", status: stWarn,
				detail: "no cap_net_admin on the launcher",
				remedy: "re-run the install one-liner (or: sudo setcap cap_net_admin,cap_sys_admin,cap_net_bind_service+ep " + self + ")"})
		}
	}
	add(check{area: "local", name: "system resolver", status: stOK,
		detail: "per-session resolv.conf (mount namespace) — nothing global to go stale"})

	// WHERE dotted names actually go. Read from what the running datapath
	// published, never re-derived from the system here: those two answers differ
	// exactly when it matters — a capture that went stale (the VPN moved after
	// the session started) looks perfectly healthy if you ask the system again.
	// This is the one place that can show the difference.
	if ups := tun.CurrentUpstreams(); len(ups) > 0 {
		d := "forwarding dotted names to " + strings.Join(ups, ", ")
		st := stOK
		for _, u := range ups {
			if strings.HasPrefix(u, "8.8.8.8") || strings.HasPrefix(u, "1.1.1.1") {
				st = stWarn
				d += " — a PUBLIC resolver: internal names will not resolve, and these lookups leave your network"
			}
		}
		add(check{area: "local", name: "dns forwarding", status: st, detail: d})
	}
}

// doctorSessions — no global registry on Linux (each launch owns its private
// datapath); nothing machine-wide to report.
func doctorSessions(add func(check)) {
	add(check{area: "local", name: "sessions", status: stOK,
		detail: "per-launch datapaths (no machine-wide state on Linux)"})
}

// resolverRestartRemedy: systemd-resolved on most distributions; on a machine
// without it, the stub is libc reading /etc/resolv.conf and there is nothing to
// restart — which the wording has to allow for.
func resolverRestartRemedy() string {
	return "sudo resolvectl flush-caches (or restart systemd-resolved); without systemd-resolved, check /etc/resolv.conf"
}
