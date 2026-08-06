//go:build windows

package tun

import (
	"fmt"
	"net/netip"

	"golang.org/x/sys/windows"
	wgtun "golang.zx2c4.com/wireguard/tun"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
)

// probeResolverAddr is where the fake VPN's resolver answers. TEST-NET-2
// (RFC 5737): assigned to nobody and routed nowhere, so an adapter carrying it
// cannot shadow a network this machine is on. It must not be a loopback address
// either — Windows resolvers never are, and pickUpstreams drops those.
const probeResolverAddr = "198.51.100.53"

// probeAdapter is the fake VPN's adapter name, distinct from plug's own so
// systemDNS's "not my LUID" rule is exercised rather than sidestepped.
const probeAdapter = "plug-vpnprobe"

// newVPNRig fakes a VPN on Windows the way a real one exists: a second WinTUN
// adapter, carrying its own resolver, with a metric that puts it ahead of the
// machine's Ethernet. That is what WireGuard and OpenVPN both do, and it is the
// exact shape systemDNS has to read correctly — adapter table, interface metric
// order, and the exclusion of plug's own adapter.
//
// Creating an adapter rather than editing the runner's own is also the safe
// choice: nothing on the machine is modified, so nothing has to be put back —
// closing the device removes the adapter and everything it announced with it.
func newVPNRig(_ []string, _ string, log logfn) (*vpnRig, error) {
	dev, err := wgtun.CreateTUN(probeAdapter, mtu)
	if err != nil {
		return nil, fmt.Errorf("create the fake VPN adapter (needs Administrator + wintun.dll): %w", err)
	}
	nt, ok := dev.(*wgtun.NativeTun)
	if !ok {
		_ = dev.Close()
		return nil, fmt.Errorf("fake VPN adapter: unexpected device type %T", dev)
	}
	luid := winipcfg.LUID(nt.LUID())
	v4 := winipcfg.AddressFamily(windows.AF_INET)
	addr, err := netip.ParseAddr(probeResolverAddr)
	if err != nil {
		_ = dev.Close()
		return nil, fmt.Errorf("fake VPN address: %w", err)
	}
	fail := func(e error) (*vpnRig, error) {
		_ = dev.Close()
		return nil, e
	}
	if err := luid.SetIPAddresses([]netip.Prefix{netip.PrefixFrom(addr, 32)}); err != nil {
		return fail(fmt.Errorf("assign %s to %s: %w", probeResolverAddr, probeAdapter, err))
	}
	// Nothing routes through this adapter — the fake resolver's address is local
	// to the machine, so those packets never leave the stack. But an adapter with
	// no reader stays operationally down, and a down adapter's nameservers are
	// (correctly) ignored: drain it so the OS reports it up, as a live VPN is.
	go func() {
		batch := dev.BatchSize()
		bufs := make([][]byte, batch)
		sizes := make([]int, batch)
		for i := range bufs {
			bufs[i] = make([]byte, headroom+65535)
		}
		for {
			if _, err := dev.Read(bufs, sizes, headroom); err != nil {
				return // closed
			}
		}
	}()
	// A VPN's resolver wins because Windows ranks interfaces by metric and the
	// VPN's is lower. Pin it, rather than hoping the automatic metric lands below
	// the runner's Ethernet: without this the probe would assert nothing on a
	// machine whose Ethernet happens to rank better.
	if row, e := luid.IPInterface(v4); e == nil {
		row.UseAutomaticMetric = false
		row.Metric = 1
		if e := row.Set(); e != nil {
			log.f("vpn probe: WARNING could not pin %s's metric — it may not outrank the machine's own: %v",
				probeAdapter, e)
		}
	} else {
		log.f("vpn probe: WARNING could not read %s's IP interface row: %v", probeAdapter, e)
	}

	return &vpnRig{
		resolverAddr: probeResolverAddr,
		announce:     func() error { return luid.SetDNS(v4, []netip.Addr{addr}, nil) },
		// The adapter stays, its resolver goes: the machine falls back to its own
		// nameservers exactly as it does when a VPN drops but its adapter lingers.
		restore: func() error { return luid.SetDNS(v4, nil, nil) },
		close: func() {
			if err := dev.Close(); err != nil {
				log.f("vpn probe: WARNING the %s adapter may be left behind: %v", probeAdapter, err)
			}
		},
	}, nil
}
