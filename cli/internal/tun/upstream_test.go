package tun

import (
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
