//go:build windows

package tun

import (
	"os/exec"
	"strings"
)

// Available reports whether the TUN data path can run on this OS. On Windows the
// device is WinTUN (needs wintun.dll alongside the binary + Administrator).
func Available() bool { return true }

const defaultTUNName = "plug0"

// checkPriv is a no-op here: WinTUN adapter creation fails with a clear error
// when not elevated, which is a better signal than a hand-rolled token check.
func checkPriv() error { return nil }

// configure assigns the adapter address, routes the fake range into it, and
// points its DNS at our loopback resolver. Windows route config is WIP — the
// dump below shows the real adapter name + address + route table so it can be
// fixed against the actual runner state instead of guesswork.
func configure(ifname, dnsAddr string, log logfn) ([]string, string, func(), error) {
	for _, cmd := range [][]string{
		// Fully NAMED params: mixing name= with positional (static 10.99…) makes
		// netsh "succeed" without assigning the address, leaving plug0 with no
		// source IP — so the 240/4 route was present but the dial was unreachable.
		{"netsh", "interface", "ipv4", "set", "address", "name=" + ifname, "source=static", "address=10.99.99.1", "mask=255.255.255.0"},
		{"netsh", "interface", "ipv4", "add", "route", "prefix=" + fakeCIDR, "interface=" + ifname, "store=active"},
		{"netsh", "interface", "ipv4", "set", "dnsservers", "name=" + ifname, "source=static", "address=" + dnsAddr, "register=none"},
	} {
		if err := run(cmd[0], cmd[1:]...); err != nil {
			log.f("tun[win]: %s failed: %v", strings.Join(cmd, " "), err)
		}
	}

	// Diagnostics: what actually landed on the box.
	for _, d := range [][]string{
		{"netsh", "interface", "show", "interface"},
		{"netsh", "interface", "ipv4", "show", "addresses", ifname},
		{"netsh", "interface", "ipv4", "show", "route"},
	} {
		out, _ := exec.Command(d[0], d[1:]...).CombinedOutput()
		log.f("tun[win-diag] %s ⇒\n%s", strings.Join(d[1:], " "), strings.TrimSpace(string(out)))
	}

	cleanup := func() {
		_ = run("netsh", "interface", "ipv4", "delete", "route", fakeCIDR, ifname)
	}
	return nil, "", cleanup, nil
}
