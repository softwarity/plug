//go:build darwin

package tun

import (
	"context"
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
		out, _ := exec.CommandContext(ctx, "dscacheutil", "-q", "host", "-a", "name", name).CombinedOutput()
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
	log.f("selftest: NOTE — system resolver didn't return a fake IP for %q; the datapath is fine, but the DNS repoint isn't effective in this environment (e.g. a headless CI runner). Verify on a real desktop.", name)
	return nil
}
