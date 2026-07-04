//go:build darwin

package tunnel

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
)

// dnsBackup persists the pre-tunnel DNS settings so they can be restored even
// if the daemon crashes (see RestoreLeftoverDNS).
const dnsBackup = "/var/run/plugd-dns.json"

// setupDNS starts the split resolver on 127.0.0.1:53 and points the active
// network services at it, returning a restore function.
//
// NOTE: on a machine with a corporate VPN that injects a higher-priority
// resolver, per-service DNS is not consulted for bare names, so this does not
// intercept — a pf-based redirect is required there (tracked separately).
func setupDNS(ctx context.Context, tr *Transport, logf Logf) (func(), error) {
	upstreams := systemResolvers()
	r := NewResolver(tr, upstreams, 0, logf)
	if _, err := r.Serve(ctx, "127.0.0.1:53"); err != nil {
		return nil, fmt.Errorf("resolver listen: %w", err)
	}

	services := activeServices()
	saved := map[string][]string{}
	for _, s := range services {
		saved[s] = currentDNS(s)
	}
	if data, err := json.Marshal(saved); err == nil {
		os.WriteFile(dnsBackup, data, 0o600)
	}
	for _, s := range services {
		exec.Command("networksetup", "-setdnsservers", s, "127.0.0.1").Run()
	}
	logf("split DNS active — %d upstream(s), %d service(s)", len(upstreams), len(services))

	return func() {
		restoreDNSMap(saved)
		os.Remove(dnsBackup)
		logf("system DNS restored")
	}, nil
}

// systemResolvers returns the host's current upstream resolvers (resolver #1
// in scutil), which the split resolver falls back to for non-cluster names.
func systemResolvers() []string {
	out, err := exec.Command("scutil", "--dns").Output()
	if err != nil {
		return nil
	}
	var ups []string
	inFirst := false
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "resolver #1"):
			inFirst = true
		case strings.HasPrefix(line, "resolver #"):
			if inFirst {
				return ups // done with the first resolver block
			}
		case inFirst && strings.HasPrefix(line, "nameserver["):
			if i := strings.Index(line, ": "); i >= 0 {
				ip := strings.TrimSpace(line[i+2:])
				if ip != "" && ip != "127.0.0.1" {
					ups = append(ups, net.JoinHostPort(ip, "53"))
				}
			}
		}
	}
	return ups
}

func restoreDNSMap(saved map[string][]string) {
	for s, dns := range saved {
		if len(dns) == 0 {
			exec.Command("networksetup", "-setdnsservers", s, "empty").Run()
		} else {
			exec.Command("networksetup", append([]string{"-setdnsservers", s}, dns...)...).Run()
		}
	}
}

// RestoreLeftoverDNS undoes a DNS redirection left behind by a crashed session.
// Called at daemon startup so a crash can never leave DNS broken.
func RestoreLeftoverDNS() {
	data, err := os.ReadFile(dnsBackup)
	if err != nil {
		return
	}
	var saved map[string][]string
	if json.Unmarshal(data, &saved) == nil {
		restoreDNSMap(saved)
	}
	os.Remove(dnsBackup)
}

func activeServices() []string {
	out, err := exec.Command("networksetup", "-listallnetworkservices").Output()
	if err != nil {
		return nil
	}
	var svcs []string
	for i, line := range strings.Split(string(out), "\n") {
		if i == 0 { // header line
			continue
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "*") { // '*' = disabled
			continue
		}
		svcs = append(svcs, line)
	}
	return svcs
}

func currentDNS(service string) []string {
	out, err := exec.Command("networksetup", "-getdnsservers", service).Output()
	if err != nil {
		return nil
	}
	s := strings.TrimSpace(string(out))
	if strings.Contains(s, "aren't any") {
		return nil
	}
	var dns []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			dns = append(dns, l)
		}
	}
	return dns
}
