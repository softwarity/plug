package tun

import (
	"context"
	"encoding/binary"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// answeredIP is the address in a reply built by answerDNS, or "" when it carries
// none (NXDOMAIN, NODATA, SERVFAIL). The A record is last and fixed-width, which
// is what makes reading it from the tail safe here.
func answeredIP(t *testing.T, r []byte) string {
	t.Helper()
	if len(r) < 12 {
		t.Fatalf("reply too short: %d bytes", len(r))
	}
	if binary.BigEndian.Uint16(r[6:]) == 0 { // ANCOUNT
		return ""
	}
	if len(r) < 4 {
		t.Fatalf("reply claims an answer but is %d bytes", len(r))
	}
	return net.IP(r[len(r)-4:]).String()
}

// useTempGraft keeps a test run off the machine's own datapath state: every
// up.set publishes where `plug doctor` reads, and a test must not rewrite what a
// live session on this machine published.
func useTempGraft(t *testing.T) {
	t.Helper()
	old := graftDir
	graftDir = t.TempDir()
	t.Cleanup(func() { graftDir = old })
}

// watchStopped closes stop and does not return until the watcher goroutine has
// actually finished — observed, not slept for. A watcher that outlives its test
// races the cleanups that follow it (graftDir is one), which shows up as a
// mystery failure in whichever test runs next.
func watchStopped(t *testing.T, stop chan struct{}, reads *atomic.Int64) {
	t.Helper()
	close(stop)
	last := reads.Load()
	quiet := waitFor(2*time.Second, func() bool {
		n := reads.Load()
		still := n == last
		last = n
		return still
	})
	if !quiet {
		t.Error("the watcher kept reading after stop — it outlives the datapath")
	}
}

// probeOn starts a resolver knowing one name, on a free loopback port. No
// privilege, no fabricated address: these tests drive upstreamDNS directly,
// which is the layer the VPN probe asserts against on a real machine.
func probeOn(t *testing.T, name, ip string) *probeResolver {
	t.Helper()
	r, err := listenProbeResolver("127.0.0.1", 0, name, net.ParseIP(ip))
	if err != nil {
		t.Fatalf("probe resolver: %v", err)
	}
	t.Cleanup(r.close)
	return r
}

// The whole probe rests on this resolver's answers being real DNS — if they were
// malformed, a client would fail to parse them and the probe would report "plug
// did not follow" for a reason that has nothing to do with plug. Assert it
// through a genuine resolver, the same code path a caller uses.
func TestTheProbeResolverSpeaksRealDNS(t *testing.T) {
	r := probeOn(t, probeName, probeIP)
	res := &net.Resolver{PreferGo: true, Dial: dialFixed(r.addr())}

	ips, err := res.LookupIP(t.Context(), "ip4", probeName)
	if err != nil || len(ips) == 0 {
		t.Fatalf("LookupIP(%s) = %v, %v — want %s", probeName, ips, err, probeIP)
	}
	if got := ips[0].String(); got != probeIP {
		t.Errorf("LookupIP(%s) = %s, want %s", probeName, got, probeIP)
	}
	if n := r.asked.Load(); n == 0 {
		t.Error("the resolver answered without being asked — impossible; the counter is broken")
	}
}

// It must know ONE name. A resolver that answered everything would make the
// probe's central assertion ("this name exists only behind the VPN") vacuous.
func TestTheProbeResolverKnowsNothingElse(t *testing.T) {
	r := probeOn(t, probeName, probeIP)
	res := &net.Resolver{PreferGo: true, Dial: dialFixed(r.addr())}

	for _, name := range []string{"example.com", "other.corp.test", strings.ToUpper(probeName) + ".x"} {
		if ips, err := res.LookupIP(t.Context(), "ip4", name); err == nil && len(ips) > 0 {
			t.Errorf("LookupIP(%s) = %v, want no answer", name, ips)
		}
	}
	// Case-insensitively, though: DNS names are, and a VPN resolver that only
	// answered the exact case would be a bug the probe would blame on plug.
	res2 := &net.Resolver{PreferGo: true, Dial: dialFixed(r.addr())}
	if ips, err := res2.LookupIP(t.Context(), "ip4", strings.ToUpper(probeName)); err != nil || len(ips) == 0 {
		t.Errorf("LookupIP(%s) = %v, %v — DNS names are case-insensitive", strings.ToUpper(probeName), ips, err)
	}
}

func dialFixed(addr string) func(ctx context.Context, network, _ string) (net.Conn, error) {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
}

// THE test for what the VPN probe proves on a real machine, minus the privilege:
// a session that is already running must follow its resolvers when they move.
// The address path (a dotted name) goes through the Go resolver, whose Dial
// reads primary() at dial time — capture it once and a session started before
// the VPN keeps asking a resolver that cannot answer, for its whole life.
func TestTheAddressPathFollowsTheUpstreamMidSession(t *testing.T) {
	useTempGraft(t)
	const name = "internal.corp.test"
	home := probeOn(t, name, "203.0.113.10") // the resolver the machine had
	vpn := probeOn(t, name, "203.0.113.20")  // the one the VPN brings
	up := newUpstream([]string{home.addr()}) // a session that started at home
	tab := newFaketab(fakeBase)
	q := query(name, 1)

	if got := answeredIP(t, answerDNS(q, tab, up, nil)); got != "203.0.113.10" {
		t.Fatalf("before the VPN: %s → %s, want the home resolver's 203.0.113.10", name, got)
	}
	up.set([]string{vpn.addr()}) // the VPN comes up
	if got := answeredIP(t, answerDNS(q, tab, up, nil)); got != "203.0.113.20" {
		t.Errorf("after the VPN came up: %s → %s, want the VPN resolver's 203.0.113.20 — "+
			"the running session did not follow", name, got)
	}
	up.set([]string{home.addr()}) // and goes away
	if got := answeredIP(t, answerDNS(q, tab, up, nil)); got != "203.0.113.10" {
		t.Errorf("after the VPN went away: %s → %s, want 203.0.113.10 back — "+
			"lookups keep going to a resolver that is gone", name, got)
	}
	if vpn.asked.Load() == 0 || home.asked.Load() == 0 {
		t.Errorf("resolvers asked: home=%d vpn=%d — both must have been used",
			home.asked.Load(), vpn.asked.Load())
	}
}

// The non-address path (SRV, MX, PTR…) is relayed verbatim rather than resolved,
// so it reads the servers through a different door — all() instead of primary().
// Both doors have to move, or half the record types keep going to the old
// resolver while addresses follow.
func TestTheRelayPathFollowsTheUpstreamToo(t *testing.T) {
	useTempGraft(t)
	const name = "srv.corp.test"
	home := probeOn(t, name, "203.0.113.10")
	vpn := probeOn(t, name, "203.0.113.20")
	up := newUpstream([]string{home.addr()})
	up.timeout = 500 * time.Millisecond
	tab := newFaketab(fakeBase)

	// SRV: relayable, and neither resolver knows it — what matters is WHICH one
	// was asked, which the counters record.
	q := query(name, 33)
	if r := answerDNS(q, tab, up, nil); r == nil {
		t.Fatal("no reply at all for an SRV query")
	}
	if home.asked.Load() == 0 {
		t.Fatal("the SRV query never reached the home resolver")
	}
	before := vpn.asked.Load()
	up.set([]string{vpn.addr()})
	if r := answerDNS(q, tab, up, nil); r == nil {
		t.Fatal("no reply at all for an SRV query after the move")
	}
	if vpn.asked.Load() == before {
		t.Error("the SRV query still went to the old resolver — the relay path does not follow")
	}
}

// A watcher that logs every tick is a watcher nobody reads. It must speak only
// when something actually moved — and it must move when it does.
func TestWatchUpstreamsAdoptsChangesAndIsSilentOtherwise(t *testing.T) {
	useTempGraft(t)
	up := newUpstream([]string{"192.168.1.1"})
	servers := make(chan []string, 4)
	current := []string{"192.168.1.1"}
	// The watcher logs from its own goroutine; the assertions read from this one.
	var mu sync.Mutex
	var lines []string
	log := logfn(func(f string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, f)
	})
	logged := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), lines...)
	}
	stop := make(chan struct{})
	var reads atomic.Int64

	read := func() []string {
		reads.Add(1)
		select {
		case s := <-servers:
			current = s
		default:
		}
		return current
	}
	go watchUpstreams(up, read, 5*time.Millisecond, log, stop)
	t.Cleanup(func() { watchStopped(t, stop, &reads) })

	// Same answer, many ticks: nothing to say.
	time.Sleep(100 * time.Millisecond)
	if got := logged(); len(got) != 0 {
		t.Errorf("watcher logged %v with nothing changed", got)
	}
	if got := up.primary(); got != "192.168.1.1:53" {
		t.Errorf("primary drifted to %s with nothing changed", got)
	}
	// The VPN comes up.
	servers <- []string{"10.8.0.1", "10.8.0.2"}
	if !waitFor(2*time.Second, func() bool { return up.primary() == "10.8.0.1:53" }) {
		t.Fatalf("watcher did not adopt the new servers: %v", up.all())
	}
	if got := logged(); len(got) != 1 {
		t.Errorf("watcher logged %d lines for one change, want exactly 1: %v", len(got), got)
	}
	if got := up.all(); len(got) != 2 || got[1] != "10.8.0.2:53" {
		t.Errorf("watcher kept %v, want both servers in order — the fallback needs the rest", got)
	}
}

// An unreadable source must not be mistaken for "no nameservers": that would
// swap the machine's resolvers for the public fallback on a transient read
// error, which is how internal names stop resolving for no visible reason.
func TestWatchUpstreamsIgnoresAnEmptyRead(t *testing.T) {
	useTempGraft(t)
	up := newUpstream([]string{"192.168.1.1"})
	stop := make(chan struct{})
	var reads atomic.Int64
	go watchUpstreams(up, func() []string { reads.Add(1); return nil }, 5*time.Millisecond, logfn(func(string, ...any) {}), stop)
	t.Cleanup(func() { watchStopped(t, stop, &reads) })

	time.Sleep(100 * time.Millisecond)
	if got := up.primary(); got != "192.168.1.1:53" {
		t.Errorf("primary = %s after empty reads, want the servers it already had", got)
	}
}

// Stopping is its own assertion, not a side effect of the tests above: the
// watcher runs for the life of a session, and one that survived teardown would
// keep reading — and republishing — for a datapath that no longer exists.
func TestWatchUpstreamsStopsWhenTold(t *testing.T) {
	useTempGraft(t)
	up := newUpstream([]string{"192.168.1.1"})
	stop := make(chan struct{})
	var reads atomic.Int64
	go watchUpstreams(up, func() []string { reads.Add(1); return nil },
		5*time.Millisecond, logfn(func(string, ...any) {}), stop)

	if !waitFor(2*time.Second, func() bool { return reads.Load() > 0 }) {
		t.Fatal("the watcher never read anything — it is not running")
	}
	watchStopped(t, stop, &reads)
}
