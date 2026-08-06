package tun

import (
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
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

// systemServers filters what the OS just published on a key plug also writes to.
// Reading such a key is the only way to see the machine's real resolvers on a
// platform where plug overwrites them — but the read comes back holding OUR
// address on every tick that follows our own write, and could hold another plug
// instance's. Adopting either points the relay at a stub that forwards to the
// stub that forwarded to it: not an error, a silent unbounded loop.
//
// own is this instance's resolver address. Anything unparseable is dropped too:
// these values come from a system store, not from us.
func systemServers(servers []string, own string) []string {
	out := make([]string, 0, len(servers))
	seen := map[string]bool{}
	for _, s := range servers {
		s = strings.TrimSpace(s)
		switch {
		case s == "", s == own, seen[s]:
			continue
		case net.ParseIP(s) == nil, inFakeRange(s):
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// upstreamPoll is how often the platforms that can re-read their source check it.
// A VPN coming up or down is a human-scale event, and the read is a local syscall
// or a small file. A var only so the VPN probe can shorten it — it is set once,
// before any watcher starts, and never while one is running.
var upstreamPoll = 10 * time.Second

// watchUpstreams re-reads the system nameservers every tick and moves the
// forwarder when they changed. Used where plug does NOT overwrite the source it
// reads — the adapter table on Windows, the untouched /etc/resolv.conf on Linux
// — so a re-read always tells the truth. macOS is different: plug overwrites the
// primary service's DNS dict, so a re-read there returns plug's own address;
// that platform hooks its existing watchdog instead, at the one moment the new
// servers are still visible.
//
// Silent unless something actually moved. every is upstreamPoll in production;
// the VPN probe drives it faster so the test does not wait on a human-scale tick.
func watchUpstreams(u *upstreamDNS, read func() []string, every time.Duration, log logfn, stop <-chan struct{}) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			servers := read()
			if len(servers) == 0 || u.same(servers) {
				continue // nothing readable, or nothing new — say nothing
			}
			u.set(servers)
			log.f("tun: the system nameservers changed — forwarding dotted names to %v", servers)
		}
	}
}

// upstreamsFile is where the running datapath publishes the servers it forwards
// to, so `plug doctor` can report what is ACTUALLY in use rather than re-reading
// the system and guessing. Those two answers differ exactly when it matters —
// a stale capture is invisible from the outside otherwise.
//
// Written best-effort next to the other datapath state, world-readable: doctor
// runs as the user while the daemon may hold root.
func upstreamsFile() string { return filepath.Join(graftDir, "upstreams") }

// publishUpstreams records the current servers. Failure is silent: this is
// reporting, and losing it must never disturb resolution.
func publishUpstreams(addrs []string) {
	if err := os.MkdirAll(graftDir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(upstreamsFile(), []byte(strings.Join(addrs, "\n")+"\n"), 0o644)
}

// CurrentUpstreams is what the running datapath last published — the servers
// dotted names are being forwarded to right now. Empty when nothing is running,
// or on a platform that keeps no shared state.
func CurrentUpstreams() []string {
	b, err := os.ReadFile(upstreamsFile())
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// ClearUpstreams drops the published record — the datapath is going away, and a
// leftover list would read as live.
func ClearUpstreams() { _ = os.Remove(upstreamsFile()) }
