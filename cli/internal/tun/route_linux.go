//go:build linux

package tun

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Available reports whether the TUN data path can run on this OS.
func Available() bool { return true }

// defaultTUNName is the interface name we ask wireguard-go to create.
const defaultTUNName = "plug0"

// capNetAdmin is the CAP_NET_ADMIN bit — the marker capability the install grants
// (alongside CAP_SYS_ADMIN + CAP_NET_BIND_SERVICE) so plug runs without sudo.
const capNetAdmin = 12

func checkPriv() error {
	if os.Geteuid() == 0 || hasEffCap(capNetAdmin) {
		return nil
	}
	return fmt.Errorf("plug needs the privileged setup — re-run the cluster install (it grants file capabilities via sudo once), or run with sudo")
}

// hasEffCap reports whether the process holds capability `bit` in its effective
// set (granted by file capabilities when the binary was setcap'd at install).
func hasEffCap(bit uint) bool {
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if v, ok := strings.CutPrefix(line, "CapEff:"); ok {
			eff, err := strconv.ParseUint(strings.TrimSpace(v), 16, 64)
			return err == nil && eff&(1<<bit) != 0
		}
	}
	return false
}

// configure brings the TUN up, routes the fake range into it, and repoints the
// child's resolver at our loopback DNS — returning the child's former upstream
// nameservers (read before the rewrite) and a cleanup that restores everything.
func configure(_ any, ifname, dnsAddr string, log logfn) ([]string, string, func(), error) {
	ups := resolvNameservers()

	for _, cmd := range [][]string{
		{"ip", "link", "set", ifname, "up"},
		{"ip", "addr", "add", "10.99.99.1/24", "dev", ifname},
		{"ip", "route", "add", fakeCIDR, "dev", ifname},
	} {
		if err := run(cmd[0], cmd[1:]...); err != nil {
			return nil, "", func() {}, err
		}
	}
	// Replies to the child arrive on the TUN with a 240/4 source; their route
	// back is the TUN itself, so strict reverse-path filtering would drop them.
	_ = run("sysctl", "-w", "net.ipv4.conf."+ifname+".rp_filter=2")
	_ = run("sysctl", "-w", "net.ipv4.conf.all.rp_filter=2")

	// A PRIVATE resolv.conf — runChild bind-mounts it over /etc/resolv.conf inside
	// the child's OWN mount namespace, so pointing the resolver at us is scoped to
	// this launch: the global /etc/resolv.conf is never modified and two `plug`
	// runs never collide.
	privResolv := ""
	if f, err := os.CreateTemp("", "plug-resolv-*.conf"); err == nil {
		_, _ = f.WriteString("nameserver " + dnsAddr + "\n")
		_ = f.Close()
		privResolv = f.Name()
	} else {
		log.f("tun: warning: private resolv.conf: %v — cluster names may not resolve", err)
	}

	cleanup := func() {
		_ = run("ip", "route", "del", fakeCIDR, "dev", ifname)
		_ = run("ip", "addr", "del", "10.99.99.1/24", "dev", ifname)
		if privResolv != "" {
			_ = os.Remove(privResolv)
		}
	}
	return ups, privResolv, cleanup, nil
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
