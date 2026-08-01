package tun

import (
	"net"
	"sort"
)

// dnsCandidate is one nameserver as the OS reported it, with everything needed
// to judge whether plug may forward to it. Filled in per platform; judged here,
// so the rules are one testable function on every OS rather than three.
type dnsCandidate struct {
	addr     string // the nameserver, no port
	metric   uint32 // interface metric — lower is the one the OS prefers
	own      bool   // it is OUR adapter's: forwarding here would loop
	up       bool   // the interface is operational
	loopback bool
}

// fakeMask15 covers 198.18.0.0/15, the range plug mints from. Nothing outside
// plug answers there.
const fakeMask15 = 0xFFFE0000

// inFakeRange reports whether an address belongs to the range plug hands out.
// A nameserver inside it is either this instance or another plug — forwarding
// there is a loop, and the loop is silent: the query comes back to us and we
// forward it again.
func inFakeRange(addr string) bool {
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	u := uint32(v4[0])<<24 | uint32(v4[1])<<16 | uint32(v4[2])<<8 | uint32(v4[3])
	return u&fakeMask15 == fakeBase&fakeMask15
}

// pickUpstreams turns what the OS reported into the servers plug will forward
// to: the ones that can actually answer, best interface first, each once.
//
// Order matters because the first is the one every relayed query goes to. The
// OS ranks its interfaces by metric and so do we — on a laptop with a corporate
// VPN up, that is the VPN's resolver, which is the only one that knows the
// internal names.
func pickUpstreams(cands []dnsCandidate) []string {
	keep := make([]dnsCandidate, 0, len(cands))
	for _, c := range cands {
		switch {
		case !c.up, c.loopback, c.own:
			continue
		case c.addr == "", inFakeRange(c.addr):
			continue
		}
		keep = append(keep, c)
	}
	// Stable, so two interfaces sharing a metric keep the order the OS listed
	// them in — that order is itself meaningful, and reshuffling it would make
	// which resolver plug uses vary between runs on the same machine.
	sort.SliceStable(keep, func(i, j int) bool { return keep[i].metric < keep[j].metric })

	out := make([]string, 0, len(keep))
	seen := map[string]bool{}
	for _, c := range keep {
		if !seen[c.addr] {
			seen[c.addr] = true
			out = append(out, c.addr)
		}
	}
	return out
}
