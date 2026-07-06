//go:build windows

package tun

import (
	"fmt"
	"net/netip"
	"os/exec"
	"strings"

	"golang.org/x/sys/windows"
	wgtun "golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// Available reports whether the TUN data path can run on this OS. On Windows the
// device is WinTUN (needs wintun.dll alongside the binary + Administrator).
func Available() bool { return true }

const defaultTUNName = "plug0"

// checkPriv is a no-op here: WinTUN adapter creation fails with a clear error
// when not elevated, which is a better signal than a hand-rolled token check.
func checkPriv() error { return nil }

// configure programs the WinTUN adapter through its LUID via the IP Helper API
// (winipcfg) — NOT netsh. netsh returns success but does not actually assign an
// address to a WinTUN adapter, which left it with no source IP (route present,
// dial "unreachable"); wireguard-windows uses winipcfg for exactly this reason.
func configure(dev any, ifname, dnsAddr string, log logfn) ([]string, string, func(), error) {
	nt, ok := dev.(*wgtun.NativeTun)
	if !ok {
		return nil, "", func() {}, fmt.Errorf("windows TUN: unexpected device type %T", dev)
	}
	luid := winipcfg.LUID(nt.LUID())
	v4 := winipcfg.AddressFamily(windows.AF_INET)

	if err := luid.SetIPAddresses([]netip.Prefix{netip.MustParsePrefix("10.99.99.1/24")}); err != nil {
		return nil, "", func() {}, fmt.Errorf("assign adapter IP: %w", err)
	}
	// On-link route for the fake range: nexthop 0.0.0.0 → send straight out plug0.
	if err := luid.AddRoute(netip.MustParsePrefix(fakeCIDR), netip.IPv4Unspecified(), 0); err != nil {
		return nil, "", func() {}, fmt.Errorf("add %s route: %w", fakeCIDR, err)
	}
	if dns, err := netip.ParseAddr(dnsAddr); err == nil {
		if e := luid.SetDNS(v4, []netip.Addr{dns}, nil); e != nil {
			log.f("tun[win]: set DNS: %v", e)
		}
	}

	// diagnostics: confirm winipcfg actually applied the IP + route.
	for _, d := range [][]string{
		{"netsh", "interface", "ipv4", "show", "addresses", ifname},
		{"netsh", "interface", "ipv4", "show", "route"},
	} {
		out, _ := exec.Command(d[0], d[1:]...).CombinedOutput()
		log.f("tun[win-diag] %s ⇒\n%s", strings.Join(d[2:], " "), strings.TrimSpace(string(out)))
	}

	cleanup := func() {
		_ = luid.FlushRoutes(v4)
		_ = luid.FlushIPAddresses(v4)
	}
	return nil, "", cleanup, nil
}
