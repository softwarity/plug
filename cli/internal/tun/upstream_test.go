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

// The watcher itself is exercised in fakevpn_test.go, against the real goroutine
// and ticker. What belongs here is the decision it rests on: two readings that
// mean the same thing must not count as a change, whatever their spelling — a
// watcher that thought they differed would log, and re-publish, every tick for
// the life of a session.
func TestASameSpelledDifferentlyIsNotAChange(t *testing.T) {
	u := newUpstream([]string{"192.168.1.1"})
	for _, same := range [][]string{{"192.168.1.1"}, {"192.168.1.1:53"}} {
		if !u.same(same) {
			t.Errorf("%v read as a change from %v", same, u.all())
		}
	}
	for _, diff := range [][]string{{"10.8.0.1"}, {"192.168.1.1", "10.8.0.1"}, {"192.168.1.1:5353"}, nil} {
		if u.same(diff) {
			t.Errorf("%v read as no change from %v", diff, u.all())
		}
	}
}

// systemServers guards the one platform where plug reads the machine's resolvers
// from a key it also WRITES: macOS. The read comes back holding our own address
// on every tick after our own write, and adopting it would point the relay at the
// stub that is doing the relaying — a loop that answers nothing and logs nothing.
func TestSystemServersNeverAdoptsUsOrAnotherPlug(t *testing.T) {
	const own = "198.18.0.53"
	for _, tc := range []struct {
		what string
		in   []string
		want []string
	}{
		{"our own address, the tick after our own write", []string{own}, nil},
		{"ours mixed in with the real ones", []string{own, "192.168.1.1"}, []string{"192.168.1.1"}},
		{"another plug instance's stub", []string{"198.18.3.53", "192.168.1.1"}, []string{"192.168.1.1"}},
		{"the far end of plug's /15", []string{"198.19.255.53"}, nil},
		{"a neighbour of the range, which is NOT ours", []string{"198.20.0.53"}, []string{"198.20.0.53"}},
		{"junk from the system store", []string{"", "  ", "not-an-ip", "192.168.1.1"}, []string{"192.168.1.1"}},
		{"the same server listed twice", []string{"10.8.0.1", "10.8.0.1"}, []string{"10.8.0.1"}},
		{"order, which is the whole answer", []string{"10.8.0.1", "192.168.1.1"}, []string{"10.8.0.1", "192.168.1.1"}},
		{"nothing at all", nil, nil},
	} {
		got := systemServers(tc.in, own)
		if len(got) != len(tc.want) {
			t.Errorf("%s: systemServers(%v) = %v, want %v", tc.what, tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: systemServers(%v) = %v, want %v", tc.what, tc.in, got, tc.want)
				break
			}
		}
	}
}

// The filter must never answer "nothing" in a way the caller could mistake for
// "the machine has no resolvers": the caller's guard is len(real) > 0, so an
// all-filtered read has to leave the forwarder exactly as it was.
func TestAnAllFilteredReadChangesNothing(t *testing.T) {
	u := newUpstream([]string{"192.168.1.1"})
	if real := systemServers([]string{"198.18.0.53"}, "198.18.0.53"); len(real) > 0 {
		u.set(real)
	}
	if got := u.primary(); got != "192.168.1.1:53" {
		t.Errorf("primary = %s, want the servers it already had", got)
	}
}

// doctor must report what the datapath IS using, not what the system says now —
// those two answers diverge exactly when it matters (a capture that went stale
// after a VPN moved looks perfectly healthy if you re-ask the system).
func TestPublishedUpstreamsRoundTrip(t *testing.T) {
	old := graftDir
	graftDir = t.TempDir()
	defer func() { graftDir = old }()

	if got := CurrentUpstreams(); len(got) != 0 {
		t.Errorf("with nothing running, CurrentUpstreams = %v, want empty", got)
	}
	u := newUpstream([]string{"10.8.0.1", "192.168.1.1"})
	got := CurrentUpstreams()
	if len(got) != 2 || got[0] != "10.8.0.1:53" {
		t.Fatalf("CurrentUpstreams = %v, want the servers set, in order", got)
	}
	// A change must be visible immediately: this is the whole point.
	u.set([]string{"1.1.1.1"})
	if got := CurrentUpstreams(); len(got) != 1 || got[0] != "1.1.1.1:53" {
		t.Errorf("after a change, CurrentUpstreams = %v", got)
	}
	ClearUpstreams()
	if got := CurrentUpstreams(); len(got) != 0 {
		t.Errorf("after clearing, CurrentUpstreams = %v, want empty", got)
	}
}

// Windows hands fec0:0:0:ffff::1/2/3 to every adapter that has no DNS of its
// own. They never answer, and relay() spends its full per-server budget on each
// before moving on: three dead servers, three timeouts, on every SRV/MX/PTR
// lookup once the real resolver goes quiet.
//
// They are dropped — and REPORTED. Dropping them declares an address family
// "not a resolver", which will be wrong on some network one day; the trace is
// what makes that discoverable instead of silent.
func TestSiteLocalV6IsDroppedAndReported(t *testing.T) {
	got, dropped := pickUpstreamsTraced([]dnsCandidate{
		up("192.168.1.1", 25),
		up("fec0:0:0:ffff::1", 25),
		up("fec0:0:0:ffff::2", 25),
		up("fec0:0:0:ffff::3", 25),
	})
	if len(got) != 1 || got[0] != "192.168.1.1" {
		t.Errorf("upstreams = %v, want only the real resolver", got)
	}
	if len(dropped) != 3 {
		t.Errorf("dropped = %v, want the three site-local ones reported so the choice is visible", dropped)
	}
}

// The rule is fec0::/10, not "any IPv6": a real IPv6 resolver must go through
// untouched, or plug would be unusable on an IPv6-only network.
func TestRealIPv6ResolversAreKept(t *testing.T) {
	got, dropped := pickUpstreamsTraced([]dnsCandidate{
		up("2001:4860:4860::8888", 10),
		up("fd00::1", 20), // unique-local: legitimate, NOT the deprecated range
	})
	if len(got) != 2 {
		t.Errorf("upstreams = %v, want both IPv6 resolvers kept", got)
	}
	if len(dropped) != 0 {
		t.Errorf("dropped = %v, want nothing — neither is site-local", dropped)
	}
}

// The boundary of fec0::/10 itself: fec0 through feff is in, fe80 (link-local)
// is not — and link-local must not be silently swept in with it.
func TestTheSiteLocalBoundaryIsExact(t *testing.T) {
	for _, in := range []string{"fec0::1", "fedc::1", "feff::1"} {
		if !siteLocalV6(in) {
			t.Errorf("%s should be site-local (fec0::/10)", in)
		}
	}
	for _, out := range []string{"fe80::1", "fd00::1", "2001:db8::1", "192.168.1.1", "not-an-ip"} {
		if siteLocalV6(out) {
			t.Errorf("%s must NOT be treated as site-local", out)
		}
	}
}
