//go:build windows

package tun

import (
	"fmt"
	"net/netip"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	wgtun "golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// Available reports whether the TUN data path can run on this OS. On Windows the
// device is WinTUN (needs wintun.dll alongside the binary + Administrator).
func Available() bool { return true }

const defaultTUNName = "plug0"

// One datapath per machine (the SYSTEM service, multiDial per flow): a single
// instance slot.
const maxInstances = 1

func tunNameFor(int) string { return defaultTUNName }

// checkPriv is a no-op here: WinTUN adapter creation fails with a clear error
// when not elevated, which is a better signal than a hand-rolled token check.
func checkPriv() error { return nil }

// configure programs the WinTUN adapter through its LUID via the IP Helper API
// (winipcfg) — NOT netsh. netsh returns success but does not actually assign an
// address to a WinTUN adapter, which left it with no source IP (route present,
// dial "unreachable"); wireguard-windows uses winipcfg for exactly this reason.
// It assigns the adapter IP, routes the instance's /24 on-link, and sets the
// adapter DNS to dnsIP (198.18.<N>.53).
func configure(dev any, _ int, _, cidr, dnsIP string, up *upstreamDNS, log logfn) ([]string, string, func(), error) {
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

	stopWatch := make(chan struct{})
	go watchUpstreams(up, func() []string { return systemDNS(luid) }, upstreamPoll, log, stopWatch)
	cleanup := func() {
		close(stopWatch)
		clearSystemNRPT(dnsIP)
		_ = luid.FlushRoutes(v4)
		_ = luid.FlushIPAddresses(v4)
	}
	// The machine's REAL nameservers, so relayed lookups go where this machine's
	// DNS was already going. Without them plug fell back to a public resolver,
	// which on a corporate network is the worst of both: the internal names it is
	// asked about do not exist there, and asking sends them off the network.
	//
	// It is not only the .plug suffix that reaches us: plug0 carries a resolver
	// address of its own, and Windows queries every interface's resolver at once
	// (smart multi-homed name resolution). Dotted names land here too, and
	// whichever answer comes back first wins — so answering them badly, or
	// quickly with NXDOMAIN, is worse than not answering at all.
	ups := systemDNS(luid)
	if len(ups) == 0 {
		log.f("tun[win]: no system nameserver found — dotted names will go to a public resolver")
	} else {
		log.f("tun[win]: forwarding dotted names to %v", ups)
	}
	return ups, "", cleanup, nil
}

// systemDNS lists the machine's nameservers, best interface first, EXCLUDING
// our own adapter — self is the one entry that must never come back, since
// forwarding to ourselves is an unbounded loop rather than an error.
//
// Read here, at configure time, from the adapter table rather than from any
// saved state: it is the same source Windows itself resolves against, and it is
// already correct when a VPN brought its own resolver up before plug started.
func systemDNS(self winipcfg.LUID) []string {
	adapters, err := winipcfg.GetAdaptersAddresses(windows.AF_UNSPEC, winipcfg.GAAFlagDefault)
	if err != nil {
		return nil
	}
	var cands []dnsCandidate
	for _, a := range adapters {
		for dns := a.FirstDNSServerAddress; dns != nil; dns = dns.Next {
			ip := dns.Address.IP()
			if ip == nil {
				continue
			}
			cands = append(cands, dnsCandidate{
				addr:     ip.String(),
				metric:   a.Ipv4Metric,
				own:      a.LUID == self,
				up:       a.OperStatus == winipcfg.IfOperStatusUp,
				loopback: a.IfType == winipcfg.IfTypeSoftwareLoopback,
			})
		}
	}
	return pickUpstreams(cands)
}

// NRPT rules live under this policy key, one subkey (a GUID) per rule. plug writes
// its own directly rather than via the Add-DnsClientNrptRule cmdlet: the cmdlet costs
// ~1.5 s just to start PowerShell, and it was run twice on every datapath bring-up —
// the bulk of the Windows cold-start latency.
const nrptConfigPath = `SOFTWARE\Policies\Microsoft\Windows NT\DNSClient\DnsPolicyConfig`
const nrptRuleName = `{6F3B2A1C-4D5E-6F70-8A9B-0C1D2E3F4A5B}` // stable id for plug's rule

// setSystemNRPT installs a Name Resolution Policy Table rule routing the ".plug"
// search suffix to dnsIP by writing DnsPolicyConfig directly (same shape the cmdlet
// encodes). Paired with that suffix on plug0 (SetDNS above), it makes single-label
// cluster names resolve on Windows: getaddrinfo appends the suffix (a real DNS query
// at last), NRPT sends ".plug" here, answerDNS strips it back. It is the Windows
// equivalent of scutil on macOS / resolv.conf on Linux, and the same suffix+NRPT
// mechanism Tailscale/WireGuard use.
func setSystemNRPT(dnsIP string) error {
	clearSystemNRPT(dnsIP) // drop a stale rule a crashed run (or an old pwsh one) may have left
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, nrptConfigPath+`\`+nrptRuleName, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	if err := k.SetDWordValue("Version", 2); err != nil {
		return err
	}
	if err := k.SetStringsValue("Name", []string{"." + searchSuffix}); err != nil {
		return err
	}
	if err := k.SetStringValue("GenericDNSServers", dnsIP); err != nil {
		return err
	}
	if err := k.SetDWordValue("ConfigOptions", 0x8); err != nil { // 0x8 = use GenericDNSServers
		return err
	}
	flushDNS() // so a prior "Could not resolve" negative doesn't stick
	return nil
}

// clearSystemNRPT removes every DnsPolicyConfig rule whose DNS server is dnsIP (ours)
// and flushes the cache. Keyed on the server IP so it also reaps stale rules from an
// older run — including the pre-registry PowerShell ones — and tolerant of none.
func clearSystemNRPT(dnsIP string) {
	base, err := registry.OpenKey(registry.LOCAL_MACHINE, nrptConfigPath, registry.READ|registry.WRITE)
	if err != nil {
		return
	}
	defer base.Close()
	subs, err := base.ReadSubKeyNames(-1)
	if err != nil {
		return
	}
	for _, sub := range subs {
		s, err := registry.OpenKey(base, sub, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		servers, _, _ := s.GetStringValue("GenericDNSServers")
		s.Close()
		if servers == dnsIP {
			_ = registry.DeleteKey(base, sub)
		}
	}
	flushDNS()
}

// flushDNS clears the resolver cache via dnsapi.dll — no external process.
func flushDNS() {
	_, _, _ = windows.NewLazySystemDLL("dnsapi.dll").NewProc("DnsFlushResolverCache").Call()
}
