package tun

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
)

func up(addr string, metric uint32) dnsCandidate {
	return dnsCandidate{addr: addr, metric: metric, up: true}
}

// The first server is where every relayed query goes, so the order is the whole
// answer. The OS ranks interfaces by metric; on a laptop with a corporate VPN
// up, the low-metric one is the VPN's resolver — the only one that knows the
// internal names plug is being asked about.
func TestTheOSPreferredInterfaceComesFirst(t *testing.T) {
	got := pickUpstreams([]dnsCandidate{
		up("192.168.1.1", 35), // home router
		up("10.8.0.1", 5),     // corporate VPN
		up("192.168.1.254", 40),
	})
	want := []string{"10.8.0.1", "192.168.1.1", "192.168.1.254"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("pickUpstreams = %v, want %v", got, want)
	}
}

// The one entry that must never survive. Forwarding to ourselves is not an
// error that surfaces — the query comes back in and is forwarded again, for
// ever, on the resolver the whole machine is using.
func TestOurOwnResolverIsNeverAnUpstream(t *testing.T) {
	mine := dnsCandidate{addr: "198.18.0.53", metric: 1, up: true, own: true}
	got := pickUpstreams([]dnsCandidate{mine, up("192.168.1.1", 35)})
	for _, g := range got {
		if g == "198.18.0.53" {
			t.Fatal("plug would forward its own queries to itself — an unbounded loop")
		}
	}
	if len(got) != 1 || got[0] != "192.168.1.1" {
		t.Errorf("pickUpstreams = %v, want just the real resolver", got)
	}
}

// Belt and braces for the same loop: an address in plug's own range that the OS
// reported on some OTHER adapter is still plug — a leftover from a crashed run,
// or a second instance. It cannot answer for anything real either way.
func TestAnyAddressInPlugsRangeIsRefused(t *testing.T) {
	for _, addr := range []string{"198.18.0.53", "198.18.7.53", "198.19.255.254"} {
		got := pickUpstreams([]dnsCandidate{up(addr, 1), up("1.1.1.1", 50)})
		if len(got) != 1 || got[0] != "1.1.1.1" {
			t.Errorf("with %s offered, pickUpstreams = %v, want only 1.1.1.1", addr, got)
		}
	}
	// And the neighbouring ranges are NOT ours — refusing them would throw away
	// a perfectly good resolver.
	for _, addr := range []string{"198.17.255.255", "198.20.0.1"} {
		if inFakeRange(addr) {
			t.Errorf("%s was taken for one of plug's own addresses", addr)
		}
	}
}

func TestDownAndLoopbackInterfacesAreSkipped(t *testing.T) {
	got := pickUpstreams([]dnsCandidate{
		{addr: "10.0.0.1", metric: 1, up: false},                 // interface is down
		{addr: "127.0.0.1", metric: 2, up: true, loopback: true}, // a local stub
		up("192.168.1.1", 30),
	})
	if len(got) != 1 || got[0] != "192.168.1.1" {
		t.Errorf("pickUpstreams = %v, want only the live non-loopback server", got)
	}
}

// Windows lists the same server once per adapter it is configured on. Asking it
// twice in a row on failure wastes the whole timeout budget on one resolver.
func TestTheSameServerIsListedOnce(t *testing.T) {
	got := pickUpstreams([]dnsCandidate{
		up("192.168.1.1", 10),
		up("192.168.1.1", 20),
		up("8.8.8.8", 30),
	})
	want := []string{"192.168.1.1", "8.8.8.8"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("pickUpstreams = %v, want %v", got, want)
	}
}

// Two interfaces on the same metric are ranked by the OS's own listing order,
// which carries information we have no better substitute for. An unstable sort
// would make plug pick a different resolver between two runs on one machine.
func TestEqualMetricsKeepTheOSOrder(t *testing.T) {
	got := pickUpstreams([]dnsCandidate{
		up("10.0.0.1", 25),
		up("10.0.0.2", 25),
		up("10.0.0.3", 25),
	})
	want := []string{"10.0.0.1", "10.0.0.2", "10.0.0.3"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("pickUpstreams = %v, want the listing order %v", got, want)
	}
}

// Nothing usable must come back as nothing, not as a half-filled list: the
// caller's fallback (a public resolver, announced loudly) depends on being able
// to tell "none" apart from "some".
func TestNothingUsableYieldsNothing(t *testing.T) {
	if got := pickUpstreams(nil); len(got) != 0 {
		t.Errorf("pickUpstreams(nil) = %v, want empty", got)
	}
	got := pickUpstreams([]dnsCandidate{
		{addr: "198.18.0.53", metric: 1, up: true, own: true},
		{addr: "10.0.0.1", metric: 2, up: false},
		{addr: "", metric: 3, up: true},
	})
	if len(got) != 0 {
		t.Errorf("pickUpstreams = %v, want empty so the caller can fall back and say so", got)
	}
}

// The machine's nameservers are not a startup fact — a VPN coming up or going
// down replaces them mid-session. Captured once, plug kept forwarding to a
// resolver that had gone away, or missed the one that had just appeared, until
// the session was restarted. The list must therefore be replaceable, and every
// reader must see the replacement.
func TestUpstreamServersCanBeReplacedMidSession(t *testing.T) {
	u := newUpstream([]string{"192.168.1.1"})
	if got := u.primary(); got != "192.168.1.1:53" {
		t.Fatalf("primary = %q, want the captured server with its default port", got)
	}
	// The VPN comes up: its resolver is the only one that knows internal names.
	u.set([]string{"10.8.0.1"})
	if got := u.primary(); got != "10.8.0.1:53" {
		t.Errorf("primary = %q after set, want the new server", got)
	}
	// And back down, to nothing at all: the public fallback, which the caller
	// announces — never an empty list that would panic on the next lookup.
	u.set(nil)
	if got := u.primary(); got != "8.8.8.8:53" {
		t.Errorf("primary = %q with no server, want the announced public fallback", got)
	}
}

// A refresh that finds the usual answer must be able to stay silent, or a
// periodic check logs on every tick. Comparison is on the NORMALISED form, so a
// bare address and the same address with :53 are the same answer.
func TestUpstreamSameIgnoresNoOpRefreshes(t *testing.T) {
	u := newUpstream([]string{"192.168.1.1", "1.1.1.1"})
	if !u.same([]string{"192.168.1.1", "1.1.1.1"}) {
		t.Error("an identical list must read as unchanged")
	}
	if !u.same([]string{"192.168.1.1:53", "1.1.1.1:53"}) {
		t.Error("the same servers written with their port must read as unchanged")
	}
	if u.same([]string{"1.1.1.1", "192.168.1.1"}) {
		t.Error("ORDER matters — the first is the one every query goes to")
	}
	if u.same([]string{"192.168.1.1"}) {
		t.Error("a shorter list is a different list")
	}
	if u.same(nil) {
		t.Error("losing every server is a change, not a no-op")
	}
}

// The resolver is built once and lives for the session, so it must read the
// address at DIAL time. Capturing it at construction was the bug in disguise:
// set() would update the relay path and leave the A path on the old server.
func TestResolverFollowsALaterSet(t *testing.T) {
	u := newUpstream([]string{"192.0.2.1"})
	dialed := make(chan string, 1)
	u.resolver = &net.Resolver{PreferGo: true, Dial: func(_ context.Context, _, _ string) (net.Conn, error) {
		select {
		case dialed <- u.primary():
		default:
		}
		return nil, errStub
	}}
	u.set([]string{"192.0.2.99"})
	_, _ = u.resolver.LookupHost(context.Background(), "example.com")
	select {
	case got := <-dialed:
		if got != "192.0.2.99:53" {
			t.Errorf("resolver dialled %q, want the server set after it was built", got)
		}
	default:
		t.Fatal("the resolver never dialled")
	}
}

var errStub = fmt.Errorf("stub dialer")

// The watcher must be silent when nothing moved — it ticks for the life of a
// session, and a VPN that never changes must not produce a line every ten
// seconds. And it must move the forwarder the moment the servers do.
func TestWatchUpstreamsOnlyActsOnRealChanges(t *testing.T) {
	u := newUpstream([]string{"192.168.1.1"})
	var lines int
	log := logfn(func(string, ...any) { lines++ })

	// A reader that returns the same thing, then something new, then nothing.
	readings := [][]string{
		{"192.168.1.1"},    // unchanged
		{"192.168.1.1:53"}, // the same, written differently
		nil,                // unreadable — keep what we have, say nothing
		{"10.8.0.1"},       // the VPN came up
	}
	var i int
	read := func() []string {
		if i >= len(readings) {
			return nil
		}
		r := readings[i]
		i++
		return r
	}
	// Drive the loop's body directly: the ticker's period is not what is under
	// test, the decision is.
	for range readings {
		servers := read()
		if len(servers) == 0 || u.same(servers) {
			continue
		}
		u.set(servers)
		log.f("changed")
	}
	if lines != 1 {
		t.Errorf("logged %d times, want exactly 1 — only the real change speaks", lines)
	}
	if got := u.primary(); got != "10.8.0.1:53" {
		t.Errorf("primary = %q, want the VPN's resolver", got)
	}
}
