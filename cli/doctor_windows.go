//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"strings"

	"golang.org/x/sys/windows/registry"

	"github.com/softwarity/plug/cli/internal/tun"
)

// doctorOS — the Windows-side checks: the SYSTEM service (exists? which binary
// does its ImagePath run, and WHICH VERSION — the one thing the per-cluster
// version mechanism does not refresh), and a stale NRPT rule with no live
// session (the resolver-left-dirty state).
func doctorOS(add func(check)) {
	// The SCM service and its binary's version.
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Services\`+tun.ServiceName, registry.QUERY_VALUE)
	if err != nil {
		add(check{area: "local", name: "service", status: stWarn,
			detail: "no plug service installed",
			remedy: "re-run the install one-liner (install-windows creates it, one UAC)"})
	} else {
		img, _, _ := k.GetStringValue("ImagePath")
		k.Close()
		// ImagePath is `"<exe>" __plug-daemon` (quoted when the path has spaces).
		bin := img
		if strings.HasPrefix(bin, `"`) {
			if end := strings.Index(bin[1:], `"`); end >= 0 {
				bin = bin[1 : 1+end]
			}
		} else if sp := strings.Index(bin, " "); sp > 0 {
			bin = bin[:sp]
		}
		detail := bin
		st, remedy := stOK, ""
		if out, err := exec.Command(bin, "version").Output(); err == nil {
			v := strings.TrimSpace(string(out))
			detail = fmt.Sprintf("v%s (%s)", v, bin)
			if v != version {
				st = stWarn
				detail += fmt.Sprintf(" — launcher is v%s", version)
				remedy = "the service keeps its install-time binary: re-run install-windows to refresh it"
			}
		}
		add(check{area: "local", name: "service", status: st, detail: detail, remedy: remedy})
	}

	// A leftover NRPT rule while nothing runs = stale resolver state — but the
	// service self-tears-down ~30s AFTER the last client, and during that
	// window a present rule is legitimate. `sc query` is readable by
	// Authenticated Users (the install ACL grants it).
	running := false
	if out, err := exec.Command("sc", "query", tun.ServiceName).Output(); err == nil {
		running = strings.Contains(string(out), "RUNNING")
	}
	sessions := 0
	for _, key := range tun.ActiveClusters() {
		sessions += tun.LiveClients(key)
	}
	if base, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Policies\Microsoft\Windows NT\DNSClient\DnsPolicyConfig`, registry.READ); err == nil {
		stale := false
		if subs, err := base.ReadSubKeyNames(-1); err == nil {
			for _, sub := range subs {
				if s, err := registry.OpenKey(base, sub, registry.QUERY_VALUE); err == nil {
					servers, _, _ := s.GetStringValue("GenericDNSServers")
					s.Close()
					if strings.HasPrefix(servers, "198.18.") {
						stale = sessions == 0 && !running
					}
				}
			}
		}
		base.Close()
		switch {
		case stale:
			add(check{area: "local", name: "system resolver", status: stFail,
				detail: "a plug NRPT rule remains with NO running service and no session (stale override)",
				remedy: "plug down (clears it), then re-check"})
		case sessions > 0:
			add(check{area: "local", name: "system resolver", status: stOK,
				detail: fmt.Sprintf("plugged (%d live client(s))", sessions)})
		case running:
			add(check{area: "local", name: "system resolver", status: stOK,
				detail: "service running, no client — teardown pending (normal)"})
		default:
			add(check{area: "local", name: "system resolver", status: stOK, detail: "untouched"})
		}
	}

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
