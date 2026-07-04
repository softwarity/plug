//go:build darwin

package tunnel

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
)

// dnsBackup persists the pre-tunnel DNS settings so they can be restored even
// if the daemon crashes (see RestoreLeftoverDNS).
const dnsBackup = "/var/run/plugd-dns.json"

// configureDNS points every active network service at resolverIP and returns a
// function that restores the previous settings.
func configureDNS(resolverIP string, logf Logf) (func(), error) {
	services := activeServices()
	saved := map[string][]string{}
	for _, s := range services {
		saved[s] = currentDNS(s)
	}
	// Persist BEFORE changing anything, so a crash can be recovered from.
	if data, err := json.Marshal(saved); err == nil {
		os.WriteFile(dnsBackup, data, 0o600)
	}
	for _, s := range services {
		exec.Command("networksetup", "-setdnsservers", s, resolverIP).Run()
	}
	logf("system DNS → tunnel on %d service(s)", len(services))

	return func() {
		restoreDNSMap(saved)
		os.Remove(dnsBackup)
		logf("system DNS restored")
	}, nil
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
		// A leading '*' marks a disabled service.
		if line == "" || strings.HasPrefix(line, "*") {
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
		return nil // no explicit servers (DHCP)
	}
	var dns []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			dns = append(dns, l)
		}
	}
	return dns
}
