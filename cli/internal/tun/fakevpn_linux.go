//go:build linux

package tun

import (
	"fmt"
	"os"
	"strings"
)

// probeResolverAddr is where the fake VPN's resolver answers. TEST-NET-2
// (RFC 5737): assigned to nobody and routed nowhere, so putting it on lo cannot
// shadow a network this machine is on — and it is deliberately NOT a loopback
// address, because a real VPN never announces one.
const probeResolverAddr = "198.51.100.53"

// newVPNRig fakes a VPN the way Linux sees one: nameservers appearing in the file
// plug follows. It redirects that file to a temporary one rather than rewriting
// the machine's own — see resolvPath. What that trades away is the assertion that
// the production path is /etc/resolv.conf; the check below buys it back the only
// way that stays safe, by reading the real thing before stepping aside.
func newVPNRig(original []string, _ string, log logfn) (*vpnRig, error) {
	if len(original) == 0 {
		return nil, fmt.Errorf("%s lists no nameserver — there is nothing to come back to, "+
			"so the probe cannot test the VPN going away", resolvFile())
	}
	if got := resolvNameservers(); len(got) == 0 {
		return nil, fmt.Errorf("%s is unreadable or lists no nameserver — that is the file plug "+
			"follows on Linux, so the probe would be testing a path production does not use", resolvFile())
	}

	// The address has to exist here before the fake resolver can bind port 53 on
	// it. /32 on lo: local delivery, no route to anywhere, nothing else affected.
	if err := run("ip", "addr", "add", probeResolverAddr+"/32", "dev", "lo"); err != nil {
		return nil, fmt.Errorf("add %s on lo: %w", probeResolverAddr, err)
	}
	dropAddr := func() {
		if err := run("ip", "addr", "del", probeResolverAddr+"/32", "dev", "lo"); err != nil {
			log.f("vpn probe: WARNING %s left on lo: %v", probeResolverAddr, err)
		}
	}

	f, err := os.CreateTemp("", "plug-vpnprobe-resolv-*.conf")
	if err != nil {
		dropAddr()
		return nil, fmt.Errorf("temp resolv.conf: %w", err)
	}
	tmp := f.Name()
	_ = f.Close()
	write := func(servers []string) error {
		var b strings.Builder
		for _, s := range servers {
			b.WriteString("nameserver " + s + "\n")
		}
		return os.WriteFile(tmp, []byte(b.String()), 0o644)
	}
	// Start from what the machine really has, so redirecting the path is a no-op
	// the watcher does not even notice: the only change it will ever see is the
	// one the probe makes on purpose.
	if err := write(original); err != nil {
		dropAddr()
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("seed temp resolv.conf: %w", err)
	}
	real := setResolvFile(tmp)

	return &vpnRig{
		resolverAddr: probeResolverAddr,
		announce:     func() error { return write([]string{probeResolverAddr}) },
		restore:      func() error { return write(original) },
		close: func() {
			setResolvFile(real) // production path first — nothing below may leave it redirected
			_ = os.Remove(tmp)
			dropAddr()
		},
	}, nil
}
