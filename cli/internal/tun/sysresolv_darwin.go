//go:build darwin

package tun

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// checkSystemResolver is a BEST-EFFORT proof that the macOS system resolver
// (dscacheutil → mDNSResponder, the getaddrinfo path) resolves a single-label
// name to a fake IP — i.e. the DNS repoint is effective. It NEVER fails the
// selftest: the datapath is already proven by the round-trip above, whereas the
// repoint depends on the machine's DNS config and on mDNSResponder sending bare
// single-label names to the primary resolver, which a headless CI runner handles
// differently than a real desktop. Each dscacheutil call is time-bounded so a
// non-resolving name can't hang the test.
func checkSystemResolver(name string, log logfn) error {
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		// HelperPath, like every other helper this package runs. It is the last
		// one that was resolved through $PATH, and it survived only because the
		// caller happens to arrive with a reduced PATH: an invariant held by
		// coincidence between two guards rather than by construction, which is
		// the shape that breaks silently when one of them moves.
		out, _ := exec.CommandContext(ctx, HelperPath("dscacheutil"), "-q", "host", "-a", "name", name).CombinedOutput()
		cancel()
		for _, line := range strings.Split(string(out), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "ip_address:"); ok {
				if ip := strings.TrimSpace(v); strings.HasPrefix(ip, "198.18.") {
					log.f("selftest: system resolver %s → %s — macOS getaddrinfo fix confirmed", name, ip)
					return nil
				}
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	// No tolerance any more, and the reason is a day lost. This note was
	// printed on EVERY macOS CI run for months, filed under "headless runner",
	// and the job stayed green. It was not the runner. A runner is a Mac in
	// plain DHCP, and on a Mac in plain DHCP plug's resolver landed
	// interface-scoped and mDNSResponder never sent it a packet: the exact bug
	// the "verify on a real desktop" was deferring to, found on a real desktop
	// the day its owner cleared their hand-typed DNS servers. A datapath that
	// getaddrinfo cannot reach is not "fine", it is the one thing every
	// application on the machine will hit.
	return fmt.Errorf("the system resolver (getaddrinfo) did not resolve %q to a fake IP: applications on this machine "+
		"cannot reach cluster names, whatever dig says. On macOS this is the resolver plug wrote landing "+
		"interface-scoped instead of unscoped", name)
}
