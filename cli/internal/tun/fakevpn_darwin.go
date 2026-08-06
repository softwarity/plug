//go:build darwin

package tun

import (
	"fmt"
	"strings"
)

// probeResolverAddr is where the fake VPN's resolver answers. TEST-NET-2
// (RFC 5737): assigned to nobody and routed nowhere, so aliasing it on lo0
// cannot shadow a network this machine is on — and it is deliberately NOT a
// loopback address, because a real VPN never announces one.
const probeResolverAddr = "198.51.100.53"

// newVPNRig fakes a VPN the way macOS actually sees one: by publishing servers
// on the primary service's State: DNS dict. That is the same key configd
// republishes the DHCP lease into and the same one a VPN client writes, so the
// probe drives the exact path the watchdog reads — including the case that used
// to be missed, where the servers move while the primary service does not.
func newVPNRig(original []string, dnsIP string, log logfn) (*vpnRig, error) {
	if len(original) == 0 {
		return nil, fmt.Errorf("this machine published no nameserver at start-up — " +
			"there is nothing to come back to, so the probe cannot test the VPN going away")
	}
	svc, err := primaryService()
	if err != nil {
		return nil, fmt.Errorf("primary network service: %w", err)
	}
	key := "State:/Network/Service/" + svc + "/DNS"

	// The address has to exist on this machine before anything can bind port 53
	// on it; macOS, unlike Linux, does not hand out the whole loopback range.
	if err := run("ifconfig", "lo0", "alias", probeResolverAddr, "netmask", "255.255.255.255"); err != nil {
		return nil, fmt.Errorf("alias %s on lo0: %w", probeResolverAddr, err)
	}
	publish := func(servers []string) error {
		return scutilSet(key, "d.init\nd.add ServerAddresses * "+strings.Join(servers, " ")+"\n")
	}
	return &vpnRig{
		resolverAddr: probeResolverAddr,
		announce:     func() error { return publish([]string{probeResolverAddr}) },
		restore:      func() error { return publish(original) },
		close: func() {
			// Order matters: hand the machine its own resolvers back FIRST, so a
			// probe that failed mid-way still leaves DNS working, then drop the
			// address. The watchdog overwrites this key with plug's own address a
			// tick later either way — what we are restoring is what it reads.
			if err := publish(original); err != nil {
				log.f("vpn probe: WARNING could not republish %v on %s: %v", original, key, err)
			}
			if err := run("ifconfig", "lo0", "-alias", probeResolverAddr); err != nil {
				log.f("vpn probe: WARNING lo0 alias %s left behind: %v", probeResolverAddr, err)
			}
		},
	}, nil
}
