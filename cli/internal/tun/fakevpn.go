package tun

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"time"
)

// The VPN probe: prove that plug FOLLOWS the machine's resolvers when they move,
// without a VPN, an account, or a network anybody has to administer.
//
// What a VPN does to a machine, reduced to the part plug reads, is one thing: an
// address carrying a resolver, announced to the OS as the one to use, that knows
// names nothing else knows. That is fabricable on all three OSes through the very
// door plug reads from — the adapter table on Windows, resolv.conf on Linux, the
// primary service's DNS dict on macOS — so the probe exercises the real code
// rather than a mock of it.
//
// The assertion is a fact, not a config read: a name that ONLY the fabricated
// resolver knows must resolve THROUGH plug's stub, to an address only it could
// have returned. That is impossible unless plug actually moved. Then the "VPN"
// goes away and the name must stop resolving — a probe that only tests the
// arrival tests half of the bug.

const (
	// probeName is dotted (a single label would be minted as a cluster name and
	// never reach an upstream) and lives under .test, which RFC 6761 reserves and
	// no real resolver may answer. If it resolves before the fake VPN is up, this
	// network answers wildcards and the probe cannot prove anything — so that is
	// checked, and said, rather than silently passing.
	probeName = "vpn-only.corp.test"
	// probeIP is in TEST-NET-3 (RFC 5737): unroutable, and returned by nothing on
	// earth except the resolver below. Getting it back IS the proof.
	probeIP = "203.0.113.77"
)

// probeResolver is the resolver the fake VPN brings with it: it knows exactly one
// name. It counts what it was asked, so the probe can also assert the query
// really travelled (an answer alone could, in principle, come from anywhere).
type probeResolver struct {
	conn  *net.UDPConn
	name  string
	ip    net.IP
	asked atomic.Int64
}

// listenProbeResolver binds a resolver knowing only name → ip on addr:port.
//
// port is 53 for the real probe and not negotiable there: the OS stores
// nameservers as bare addresses, so 53 is what plug will dial. The unit tests
// pass 0 — they drive upstreamDNS directly, where an address may carry a port,
// and so need neither a privileged port nor an address to fabricate.
func listenProbeResolver(addr string, port int, name string, ip net.IP) (*probeResolver, error) {
	bind := net.ParseIP(addr)
	if bind == nil {
		return nil, fmt.Errorf("probe resolver: %q is not an address", addr)
	}
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: bind, Port: port})
	if err != nil {
		return nil, fmt.Errorf("probe resolver listen on %s:%d: %w", addr, port, err)
	}
	r := &probeResolver{conn: c, name: name, ip: ip}
	go r.serve()
	return r, nil
}

// addr is where this resolver answers, as upstreamDNS wants it.
func (r *probeResolver) addr() string { return r.conn.LocalAddr().String() }

func (r *probeResolver) serve() {
	buf := make([]byte, 4096)
	for {
		n, from, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			return // closed
		}
		r.asked.Add(1)
		if reply := answerOne(buf[:n], r.name, r.ip); reply != nil {
			_, _ = r.conn.WriteToUDP(reply, from)
		}
	}
}

func (r *probeResolver) close() { _ = r.conn.Close() }

// answerOne answers A name → ip, and NXDOMAIN to everything else — including
// name's AAAA, so a client asking for both does not wait on a record that will
// never come.
func answerOne(q []byte, name string, ip net.IP) []byte {
	if len(q) < 13 {
		return nil
	}
	asked, p := parseName(q, 12)
	if p+4 > len(q) {
		return nil
	}
	qtype := binary.BigEndian.Uint16(q[p:])
	qend := p + 4

	hit := qtype == 1 && strings.EqualFold(strings.TrimSuffix(asked, "."), name)
	r := make([]byte, 0, 64)
	r = append(r, q[0], q[1]) // id
	r = append(r, 0x81)       // QR=1, RD
	if hit {
		r = append(r, 0x80, 0, 1, 0, 1) // RA rcode 0, QDCOUNT 1, ANCOUNT 1
	} else {
		r = append(r, 0x83, 0, 1, 0, 0) // RA + NXDOMAIN, QDCOUNT 1, ANCOUNT 0
	}
	r = append(r, 0, 0, 0, 0)    // NS/AR counts
	r = append(r, q[12:qend]...) // question, verbatim
	if hit {
		r = append(r, 0xC0, 0x0C)  // name pointer
		r = append(r, 0, 1, 0, 1)  // A, IN
		r = append(r, 0, 0, 0, 5)  // TTL 5s
		r = append(r, 0, 4)        // RDLENGTH
		r = append(r, ip.To4()...) // the answer nothing else can give
	}
	return r
}

// vpnRig is one platform's way of faking a VPN. Split in four steps because each
// has to be undoable on its own: the address may be up with nothing announced
// (the state the machine must come back to), and a probe that dies between two
// steps must still leave the machine as it found it.
type vpnRig struct {
	resolverAddr string       // where the fake resolver listens, and what the OS is told
	announce     func() error // "the VPN came up": the OS now names resolverAddr
	restore      func() error // "the VPN went away": the machine's own resolvers come back
	close        func()       // drop the address/adapter, undo everything
}

// resolveThroughPlug asks plug's own stub, through the TUN, exactly as a child
// process would. Returns the address, or "" when the name does not resolve.
func resolveThroughPlug(dnsIP, name string, timeout time.Duration) string {
	res := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, net.JoinHostPort(dnsIP, "53"))
	}}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ips, err := res.LookupIP(ctx, "ip4", name)
	if err != nil || len(ips) == 0 {
		return ""
	}
	return ips[0].String()
}

// holdUntil re-asserts hold about once a second until cond holds.
//
// Announcing once is not enough on macOS: configd republishes the DHCP lease
// onto the very key a VPN writes its servers to, roughly twice a minute, and one
// landing between the announcement and the watchdog's next tick would quietly
// undo it. The probe would then fail for the machine's own housekeeping rather
// than for anything about plug. Re-asserting is idempotent everywhere else.
func holdUntil(budget time.Duration, hold func() error, cond func() bool) error {
	deadline := time.Now().Add(budget)
	var last error
	for {
		if err := hold(); err != nil {
			last = err
		}
		if waitFor(time.Second, cond) {
			return nil
		}
		if time.Now().After(deadline) {
			if last != nil {
				return fmt.Errorf("gave up after %s, last error: %w", budget, last)
			}
			return fmt.Errorf("gave up after %s", budget)
		}
	}
}

// waitFor polls until cond holds or the budget runs out. Polling, not a fixed
// sleep: the platforms notice at different rates (a 3s watchdog on macOS, the
// upstreamPoll ticker elsewhere) and a sleep long enough for the slowest would
// make the probe the longest part of the self-test.
func waitFor(budget time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(budget)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// probeVPNFollowing runs the whole probe against the LIVE datapath: plug is up,
// its stub is answering, and the machine's resolvers move under it.
//
// original is what plug captured at start-up — the resolvers to come back to.
//
// Teardown goes through addUndo rather than defer: this probe leaves an address
// (and an adapter on Windows) on the machine, and a defer does not run when a
// signal takes the process down. The caller's stack does, and in reverse order.
func probeVPNFollowing(up *upstreamDNS, dnsIP string, original []string, addUndo func(func()), log logfn) error {
	rig, err := newVPNRig(original, dnsIP, log)
	if err != nil {
		return fmt.Errorf("vpn probe: bring up the fake VPN: %w", err)
	}
	addUndo(rig.close)

	resolver, err := listenProbeResolver(rig.resolverAddr, 53, probeName, net.ParseIP(probeIP))
	if err != nil {
		return fmt.Errorf("vpn probe: %w", err)
	}
	addUndo(resolver.close)

	// Before anything moves, probeName must NOT resolve. Two things would make the
	// proof worthless and both are silent: a network that answers wildcards, and a
	// leftover fake VPN from an earlier run. Fail here, with the reason, rather
	// than pass later for the wrong reason.
	if got := resolveThroughPlug(dnsIP, probeName, 3*time.Second); got != "" {
		return fmt.Errorf("vpn probe: %s already resolves to %s before the fake VPN is up — "+
			"this resolver answers names that do not exist, so the probe cannot prove plug moved", probeName, got)
	}
	log.f("vpn probe: %s does not resolve yet (as it must not) — bringing the fake VPN up on %s",
		probeName, rig.resolverAddr)

	// The VPN comes up.
	want := net.JoinHostPort(rig.resolverAddr, "53")
	if err := holdUntil(30*time.Second, rig.announce, func() bool { return up.primary() == want }); err != nil {
		return fmt.Errorf("vpn probe: plug did not follow — still forwarding to %v, expected %s. "+
			"The system was told to use %s; plug kept the servers it captured at start-up (%v)",
			up.all(), want, rig.resolverAddr, err)
	}
	log.f("vpn probe: plug followed — forwarding dotted names to %v", up.all())

	// What doctor will report has to move too: it reads the published record, and
	// a record that goes stale is exactly the failure it exists to catch.
	if pub := CurrentUpstreams(); len(pub) == 0 || pub[0] != want {
		return fmt.Errorf("vpn probe: the datapath forwards to %s but published %v — "+
			"`plug doctor` would report the wrong resolver", want, pub)
	}

	// The proof: a name only the fake resolver knows, resolved through plug's stub.
	if got := resolveThroughPlug(dnsIP, probeName, 5*time.Second); got != probeIP {
		return fmt.Errorf("vpn probe: %s resolved to %q through plug, want %s — "+
			"plug names the right resolver but does not reach it", probeName, got, probeIP)
	}
	if n := resolver.asked.Load(); n == 0 {
		return fmt.Errorf("vpn probe: %s resolved to %s but the fake resolver was never asked — "+
			"the answer came from somewhere else", probeName, probeIP)
	}
	log.f("vpn probe: %s → %s through the stub, from the VPN's resolver (%d queries) — the internal name works",
		probeName, probeIP, resolver.asked.Load())

	// The VPN goes away. Following it up and never following it down is the same
	// bug seen from the other side: dotted names would keep going to a resolver
	// that is no longer reachable, which fails slowly instead of failing.
	if err := holdUntil(30*time.Second, rig.restore, func() bool { return up.primary() != want }); err != nil {
		return fmt.Errorf("vpn probe: the fake VPN went away but plug still forwards to %v — "+
			"lookups now go to a resolver that is gone (%v)", up.all(), err)
	}
	if len(original) > 0 {
		back := net.JoinHostPort(original[0], "53")
		if up.primary() != back {
			return fmt.Errorf("vpn probe: after the fake VPN went away plug forwards to %v, "+
				"expected the machine's own %s", up.all(), back)
		}
	}
	// And the internal name is gone with it — the negative side of the same fact.
	if got := resolveThroughPlug(dnsIP, probeName, 3*time.Second); got != "" {
		return fmt.Errorf("vpn probe: %s still resolves to %s after the fake VPN went away", probeName, got)
	}
	log.f("vpn probe: the fake VPN went away — back to %v, and %s stopped resolving", up.all(), probeName)
	return nil
}
