package tun

import (
	"net"
	"strings"
)

// nrptScopes is the pure half of scopedResolversNRPT: one registry rule's names
// and servers, as the table stores them, into scoped upstreams. Split out so the
// shapes NRPT produces - a leading dot on the name, ";" between servers, a rule
// that is plug's own - are proven on every OS and not only where the registry is.
func nrptScopes(names []string, servers, own string) []scopedUpstream {
	var addrs []string
	for _, s := range strings.Split(servers, ";") {
		s = strings.TrimSpace(s)
		if s == "" || s == own || net.ParseIP(s) == nil || inFakeRange(s) {
			continue
		}
		addrs = append(addrs, s)
	}
	if len(addrs) == 0 {
		return nil
	}
	var out []scopedUpstream
	for _, n := range names {
		d := strings.ToLower(strings.Trim(strings.TrimSpace(n), "."))
		if d == "" || d == searchSuffix {
			continue
		}
		out = append(out, scopedUpstream{domain: d, addrs: addrs})
	}
	return out
}
