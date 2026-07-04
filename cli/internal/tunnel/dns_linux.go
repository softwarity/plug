//go:build linux

package tunnel

import (
	"os"
)

const resolvConf = "/etc/resolv.conf"
const dnsBackup = "/var/run/plugd-resolv.backup"

// configureDNS rewrites /etc/resolv.conf to point at resolverIP and returns a
// function that restores the original. (systemd-resolved setups may need
// per-interface config; resolv.conf is the portable baseline.)
func configureDNS(resolverIP string, logf Logf) (func(), error) {
	orig, err := os.ReadFile(resolvConf)
	if err != nil {
		return nil, err
	}
	os.WriteFile(dnsBackup, orig, 0o600)
	if err := os.WriteFile(resolvConf, []byte("# plug tunnel\nnameserver "+resolverIP+"\n"), 0o644); err != nil {
		return nil, err
	}
	logf("system DNS → tunnel")

	return func() {
		os.WriteFile(resolvConf, orig, 0o644)
		os.Remove(dnsBackup)
		logf("system DNS restored")
	}, nil
}

// RestoreLeftoverDNS restores resolv.conf from a backup left by a crashed
// session. Called at daemon startup.
func RestoreLeftoverDNS() {
	data, err := os.ReadFile(dnsBackup)
	if err != nil {
		return
	}
	os.WriteFile(resolvConf, data, 0o644)
	os.Remove(dnsBackup)
}
