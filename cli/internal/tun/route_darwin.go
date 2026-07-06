//go:build darwin

package tun

import (
	"fmt"
	"os"
	"strings"
)

// Available reports whether the TUN data path can run on this OS.
func Available() bool { return true }

// defaultTUNName: wireguard-go assigns the next free utunN when given "utun".
const defaultTUNName = "utun"

func checkPriv() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("plug --tun needs root (create utun + set routes): run with sudo, or install the plug helper")
	}
	return nil
}

// configure brings the utun up as a point-to-point link, routes the fake range
// into it, and repoints the resolver at our loopback DNS. macOS resolves via
// SystemConfiguration, not /etc/resolv.conf, so the DNS rewrite is best-effort
// (it covers Go's pure resolver and most CLI tools).
// macOS has no mount namespaces, so the resolver repoint is still global here
// (restored on cleanup); privResolv is empty. Scoped per-launch DNS on macOS is a
// separate problem (no netns) tracked for later.
func configure(_ any, ifname, dnsAddr string, log logfn) ([]string, string, func(), error) {
	ups := resolvNameservers()

	for _, cmd := range [][]string{
		{"ifconfig", ifname, "inet", "10.99.99.1", "10.99.99.2", "up"},
		{"route", "-n", "add", "-net", fakeCIDR, "-interface", ifname},
	} {
		if err := run(cmd[0], cmd[1:]...); err != nil {
			return nil, "", func() {}, err
		}
	}

	old, _ := os.ReadFile("/etc/resolv.conf")
	if err := os.WriteFile("/etc/resolv.conf", []byte("nameserver "+dnsAddr+"\n"), 0o644); err != nil {
		log.f("tun: warning: could not repoint /etc/resolv.conf (%v) — cluster names may not resolve", err)
	}

	cleanup := func() {
		_ = run("route", "-n", "delete", "-net", fakeCIDR, "-interface", ifname)
		if old != nil {
			_ = os.WriteFile("/etc/resolv.conf", old, 0o644)
		}
	}
	return ups, "", cleanup, nil
}

func resolvNameservers() []string {
	b, err := os.ReadFile("/etc/resolv.conf")
	if err != nil {
		return nil
	}
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "nameserver" {
			out = append(out, f[1])
		}
	}
	return out
}
