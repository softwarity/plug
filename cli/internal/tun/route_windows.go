//go:build windows

package tun

import (
	"fmt"
	"net/netip"
	"os/exec"

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
// It assigns the adapter IP, routes the instance's /24 on-link, and sets the
// adapter DNS to dnsIP (198.18.<N>.53).
func configure(dev any, _, cidr, dnsIP string, log logfn) ([]string, string, func(), error) {
	nt, ok := dev.(*wgtun.NativeTun)
	if !ok {
		return nil, "", func() {}, fmt.Errorf("windows TUN: unexpected device type %T", dev)
	}
	luid := winipcfg.LUID(nt.LUID())
	v4 := winipcfg.AddressFamily(windows.AF_INET)

	if err := luid.SetIPAddresses([]netip.Prefix{netip.MustParsePrefix("10.99.99.1/24")}); err != nil {
		return nil, "", func() {}, fmt.Errorf("assign adapter IP: %w", err)
	}
	// On-link route for the instance's /24: nexthop 0.0.0.0 → send straight out plug0.
	if err := luid.AddRoute(netip.MustParsePrefix(cidr), netip.IPv4Unspecified(), 0); err != nil {
		return nil, "", func() {}, fmt.Errorf("add %s route: %w", cidr, err)
	}
	if dns, err := netip.ParseAddr(dnsIP); err == nil {
		if e := luid.SetDNS(v4, []netip.Addr{dns}, nil); e != nil {
			log.f("tun[win]: set DNS: %v", e)
		}
	}
	// Adapter DNS alone doesn't cover SINGLE-LABEL names (my-service): Windows
	// devolution + interface ordering skip it, so getaddrinfo("my-service") never
	// reaches our resolver ("Could not resolve host"). An NRPT catch-all rule points
	// ALL name resolution at dnsIP — the Windows counterpart of the macOS scutil
	// repoint. Our in-stack DNS answers cluster names with a fake IP and forwards the
	// rest to the saved upstreams, so a machine-wide rule is safe.
	if err := setSystemNRPT(dnsIP); err != nil {
		log.f("tun[win]: NRPT repoint failed — single-label names may not resolve: %v", err)
	} else {
		log.f("tun[win]: system DNS repointed to %s (NRPT catch-all)", dnsIP)
	}

	cleanup := func() {
		clearSystemNRPT(dnsIP)
		_ = luid.FlushRoutes(v4)
		_ = luid.FlushIPAddresses(v4)
	}
	return nil, "", cleanup, nil
}

// setSystemNRPT installs a catch-all Name Resolution Policy Table rule (namespace
// ".") sending every DNS query to dnsIP. This is what makes single-label cluster
// names resolve on Windows — the equivalent of scutil on macOS / resolv.conf on
// Linux. Driven through the DnsClient cmdlets, which encode the DnsPolicyConfig
// registry correctly (the same table WireGuard-Windows writes by hand), and flush
// the resolver cache so a prior negative answer ("Could not resolve") doesn't stick.
func setSystemNRPT(dnsIP string) error {
	clearSystemNRPT(dnsIP) // drop a stale rule a crashed run may have left
	return psRun("Add-DnsClientNrptRule -Namespace '.' -NameServers '" + dnsIP + "'; Clear-DnsClientCache")
}

// clearSystemNRPT removes the rule(s) pointing at dnsIP and flushes the cache. Keyed
// on the server IP so it targets only ours, and tolerant of there being none.
func clearSystemNRPT(dnsIP string) {
	_ = psRun("Get-DnsClientNrptRule | Where-Object { $_.NameServers -contains '" + dnsIP + "' } | Remove-DnsClientNrptRule -Force; Clear-DnsClientCache")
}

func psRun(script string) error {
	return exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Run()
}
