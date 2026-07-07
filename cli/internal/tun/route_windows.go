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
	// Advertise the search suffix on plug0 too: Windows won't DNS-query a BARE
	// single-label name (LLMNR/NetBIOS only), so getaddrinfo must be nudged to try
	// "my-service.<suffix>" — that's what turns it into a real DNS query.
	if dns, err := netip.ParseAddr(dnsIP); err == nil {
		if e := luid.SetDNS(v4, []netip.Addr{dns}, []string{searchSuffix}); e != nil {
			log.f("tun[win]: set DNS: %v", e)
		}
	}
	// Route that suffix to our resolver via NRPT (the Windows counterpart of the
	// macOS scutil repoint; the same mechanism Tailscale/WireGuard use). ".<suffix>"
	// is surgical — only "*.<suffix>" goes in-stack, real internet DNS is untouched —
	// and answerDNS strips the suffix back to the bare cluster name.
	if err := setSystemNRPT(dnsIP); err != nil {
		log.f("tun[win]: NRPT repoint failed — single-label names may not resolve: %v", err)
	} else {
		log.f("tun[win]: system DNS repointed — *.%s → %s (NRPT)", searchSuffix, dnsIP)
	}

	cleanup := func() {
		clearSystemNRPT(dnsIP)
		_ = luid.FlushRoutes(v4)
		_ = luid.FlushIPAddresses(v4)
	}
	return nil, "", cleanup, nil
}

// setSystemNRPT installs a Name Resolution Policy Table rule routing the ".plug"
// search suffix to dnsIP. Paired with that suffix on plug0 (SetDNS above), it makes
// single-label cluster names resolve on Windows: getaddrinfo appends the suffix (a
// real DNS query at last), NRPT sends ".plug" here, answerDNS strips it back. It is
// the Windows equivalent of scutil on macOS / resolv.conf on Linux, and the same
// suffix+NRPT mechanism Tailscale/WireGuard use. Driven through the DnsClient cmdlets
// (they encode the DnsPolicyConfig registry correctly), flushing the resolver cache
// so a prior "Could not resolve" negative doesn't stick.
func setSystemNRPT(dnsIP string) error {
	clearSystemNRPT(dnsIP) // drop a stale rule a crashed run may have left
	return psRun("Add-DnsClientNrptRule -Namespace '." + searchSuffix + "' -NameServers '" + dnsIP + "'; Clear-DnsClientCache")
}

// clearSystemNRPT removes the rule(s) pointing at dnsIP and flushes the cache. Keyed
// on the server IP so it targets only ours, and tolerant of there being none.
func clearSystemNRPT(dnsIP string) {
	_ = psRun("Get-DnsClientNrptRule | Where-Object { $_.NameServers -contains '" + dnsIP + "' } | Remove-DnsClientNrptRule -Force; Clear-DnsClientCache")
}

func psRun(script string) error {
	return exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Run()
}
