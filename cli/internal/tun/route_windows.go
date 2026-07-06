//go:build windows

package tun

// Available reports whether the TUN data path can run on this OS. On Windows the
// device is WinTUN (needs wintun.dll alongside the binary + Administrator).
func Available() bool { return true }

const defaultTUNName = "plug0"

// checkPriv is a no-op here: WinTUN adapter creation fails with a clear error
// when not elevated, which is a better signal than a hand-rolled token check.
func checkPriv() error { return nil }

// configure assigns the adapter address, routes the fake range into it, and
// points its DNS at our loopback resolver, via netsh.
func configure(ifname, dnsAddr string, log logfn) ([]string, string, func(), error) {
	for _, cmd := range [][]string{
		{"netsh", "interface", "ipv4", "set", "address", "name=" + ifname, "static", "10.99.99.1", "255.255.255.0"},
		// on-link route for the fake range via the adapter (named params + active store)
		{"netsh", "interface", "ipv4", "add", "route", "prefix=" + fakeCIDR, "interface=" + ifname, "store=active"},
		{"netsh", "interface", "ipv4", "set", "dnsservers", "name=" + ifname, "static", dnsAddr, "primary"},
	} {
		if err := run(cmd[0], cmd[1:]...); err != nil {
			return nil, "", func() {}, err
		}
	}
	cleanup := func() {
		_ = run("netsh", "interface", "ipv4", "delete", "route", fakeCIDR, ifname)
	}
	// Windows sets DNS per-adapter (netsh above), not via a global file — no
	// mount-namespace trick needed; privResolv is empty.
	return nil, "", cleanup, nil
}
