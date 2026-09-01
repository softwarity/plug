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
		// Same rule as macOS: how to stop it is a FACT about a running service,
		// stated on its line — not a remedy. As a remedy it reads as the fix for
		// whatever is wrong, and it almost never is.
		detail += " — plug down stops it while it runs"
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
			// `plug down` was advised here and could not possibly work: this state
			// is DEFINED by no service running, so there is nothing for it to stop
			// and the rule stays exactly where it is. --fix removes it directly,
			// which is safe precisely because no datapath is up to own it.
			if doctorFix {
				tun.ClearOrphanNRPT()
				add(check{area: "local", name: "system resolver", status: stOK,
					detail: "a plug NRPT rule was left behind by a service that is gone — removed"})
			} else {
				add(check{area: "local", name: "system resolver", status: stFail,
					detail: "a plug NRPT rule remains with NO running service and no session (stale override)",
					remedy: "plug doctor --fix (removes it)"})
			}
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

	doctorDNSForwarding(add)
}

// resolverRestartRemedy: the DNS Client service holds the cache Windows answers
// from.
func resolverRestartRemedy() string {
	return "ipconfig /flushdns (elevated: Restart-Service Dnscache)"
}
