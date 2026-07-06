//go:build darwin

package tun

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// checkSystemResolver proves the macOS fix end to end: it asks the SYSTEM
// resolver (dscacheutil → mDNSResponder, the path getaddrinfo and real apps take,
// the one that ignores /etc/resolv.conf) to resolve a single-label name and
// checks it returns a fake IP in our range. That is the exact ENOTFOUND bug —
// fixed by repointing the primary service's DNS at our in-netstack resolver. It
// retries briefly because configd propagates the dynamic-store change async.
func checkSystemResolver(name string, log logfn) error {
	var last string
	for i := 0; ; i++ {
		out, _ := exec.Command("dscacheutil", "-q", "host", "-a", "name", name).CombinedOutput()
		last = strings.TrimSpace(string(out))
		for _, line := range strings.Split(string(out), "\n") {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), "ip_address:"); ok {
				if ip := strings.TrimSpace(v); strings.HasPrefix(ip, "198.18.") {
					log.f("selftest: system resolver %s → %s — macOS getaddrinfo fix confirmed", name, ip)
					return nil
				}
			}
		}
		if i >= 5 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("system resolver returned no fake IP for %q — the macOS DNS repoint is not effective (mDNSResponder still using the old resolver?):\n%s", name, last)
}
