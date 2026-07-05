//go:build linux

package tun

import (
	"fmt"
	"os"
	"strings"
)

// Available reports whether the TUN data path can run on this OS.
func Available() bool { return true }

// defaultTUNName is the interface name we ask wireguard-go to create.
const defaultTUNName = "plug0"

func checkPriv() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("plug --tun needs root (create TUN + set routes): run with sudo, or install the plug helper")
	}
	return nil
}

// configure brings the TUN up, routes the fake range into it, and repoints the
// child's resolver at our loopback DNS — returning the child's former upstream
// nameservers (read before the rewrite) and a cleanup that restores everything.
func configure(ifname, dnsAddr string, log logfn) ([]string, func(), error) {
	ups := resolvNameservers()

	for _, cmd := range [][]string{
		{"ip", "link", "set", ifname, "up"},
		{"ip", "addr", "add", "10.99.99.1/24", "dev", ifname},
		{"ip", "route", "add", fakeCIDR, "dev", ifname},
	} {
		if err := run(cmd[0], cmd[1:]...); err != nil {
			return nil, func() {}, err
		}
	}
	// Replies to the child arrive on the TUN with a 240/4 source; their route
	// back is the TUN itself, so strict reverse-path filtering would drop them.
	_ = run("sysctl", "-w", "net.ipv4.conf."+ifname+".rp_filter=2")
	_ = run("sysctl", "-w", "net.ipv4.conf.all.rp_filter=2")

	old, _ := os.ReadFile("/etc/resolv.conf")
	if err := os.WriteFile("/etc/resolv.conf", []byte("nameserver "+dnsAddr+"\n"), 0o644); err != nil {
		log.f("tun: warning: could not repoint /etc/resolv.conf (%v) — cluster names may not resolve", err)
	}

	cleanup := func() {
		_ = run("ip", "route", "del", fakeCIDR, "dev", ifname)
		_ = run("ip", "addr", "del", "10.99.99.1/24", "dev", ifname)
		if old != nil {
			_ = os.WriteFile("/etc/resolv.conf", old, 0o644)
		}
	}
	return ups, cleanup, nil
}

// resolvNameservers returns the `nameserver` entries currently in resolv.conf.
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
