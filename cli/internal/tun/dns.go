package tun

import (
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	// 198.18.0.0/15 — the RFC 2544 benchmarking range: nothing routes it for
	// real, and unlike class-E 240/4 the Windows TCP/IP stack actually routes it
	// (Windows rejects 240/4 as a martian at connect(), which left the TUN
	// unreachable there). plug carves this range into per-instance /24s: instance
	// N owns 198.18.<N>.0/24 and serves its DNS on 198.18.<N>.53.
	fakeBase = 0xC6120000 // 198.18.0.0
	dnsHost  = 53         // reserved host byte: this instance's DNS is base|53
)

// searchSuffix is the DNS search suffix plug0 advertises on Windows so the OS
// actually issues a DNS query for a single-label cluster name. Windows resolves a
// BARE name (my-service) via LLMNR/NetBIOS, never DNS — so getaddrinfo never reaches
// our resolver. With this suffix in the search list, getaddrinfo tries
// "my-service.plug"; an NRPT rule routes ".plug" to our resolver, and answerDNS
// strips it back to the bare name. No effect on macOS/Linux (nothing appends it).
const searchSuffix = "plug"

// clusterLongName recognises a Kubernetes Service named the long way and gives
// it back normalised, lower-case and without the trailing dot:
// rabbitmq.shop.svc.cluster.local, or the shorter rabbitmq.shop.svc.
// The shape is fixed by Kubernetes itself, <service>.<namespace>.svc[.<cluster
// domain>], so the third label is the marker and the cluster domain (which a
// cluster may rename) is whatever follows.
//
// A process that inherits the environment of the pod it replaces gets its
// peers under this form, because that is how a Helm chart tends to write
// them: `RABBITMQ_HOST=rabbitmq.shop.svc.cluster.local`. Relayed
// upstream like any other dotted name, it came back NXDOMAIN from a resolver
// that has never heard of cluster.local, and the service started with its
// secrets in hand and no broker to talk to.
//
// The name is kept WHOLE, checked and minted as it was asked, so the agent
// dials exactly it: the pod's resolver knows the namespace and the cluster
// domain, and a Service of another namespace is reached the way the pod would
// reach it. Cutting it down to its first label would have been simpler and
// wrong, the same name in this namespace when the pod meant another one.
//
// The two-label form, rabbitmq.shop, is left alone: nothing tells it
// from a name in somebody's real domain, and this stub answers the whole
// machine on macOS.
func clusterLongName(name string) (string, bool) {
	n := strings.ToLower(strings.TrimSuffix(name, "."))
	labels := strings.Split(n, ".")
	if len(labels) < 3 || labels[2] != "svc" || labels[0] == "" || labels[1] == "" {
		return "", false
	}
	return n, true
}

// faketab maps minted fake IPs to cluster names, all within ONE instance's
// 198.18.<N>.0/24. The DNS forwarder mints; the netstack TCP forwarder looks up.
// In-process, shared, mutex-guarded.
type faketab struct {
	mu   sync.Mutex
	base uint32 // 198.18.<N>.0 — this instance's subnet
	byIP map[uint32]string
	seen map[uint32]time.Time // last time each fake was minted or dialled
	next uint32               // next host byte to hand out (1..254)
}

// reuseFloor is how long a fake must have gone untouched before it can be handed
// to a different name. The A answers carry a 5s TTL, so anything past that is
// not in a client's cache any more; a minute is an order of magnitude beyond,
// and still short enough that a long-lived daemon recovers its whole subnet.
const reuseFloor = time.Minute

func newFaketab(base uint32) *faketab {
	return &faketab{base: base, byIP: map[uint32]string{}, seen: map[uint32]time.Time{}, next: 1}
}

// dnsIP is this instance's reserved DNS address (198.18.<N>.53). It is never
// minted for a service, so the resolver IP can't be aliased by a cluster name.
func (t *faketab) dnsIP() uint32 { return t.base | dnsHost }

// mint returns a stable fake IP inside this instance's /24 for name, or 0 if the
// /24 is exhausted (254 services). The reserved DNS host (.53) is skipped; .0 and
// .255 never occur since next stays in 1..254.
func (t *faketab) mint(name string) uint32 {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	for ip, n := range t.byIP {
		if n == name {
			t.seen[ip] = now
			return ip
		}
	}
	if t.next == dnsHost {
		t.next++
	}
	if t.next > 254 {
		// The subnet was a one-way street: 254 names and every later one got
		// NXDOMAIN for ever, even after the tunnel came back. On a daemon that
		// lives for days that is not exotic — macOS routes EVERY single-label
		// lookup here, and a browser's anti-hijack probes alone are three random
		// names per network change. Recycle the coldest fake instead, but only
		// one nobody has touched in a while: reassigning an address a client
		// still has cached would send its traffic to another service.
		ip := t.coldest(now)
		if ip == 0 {
			return 0 // every fake is in recent use — refusing is the honest answer
		}
		delete(t.byIP, ip)
		t.byIP[ip] = name
		t.seen[ip] = now
		return ip
	}
	ip := t.base | t.next
	t.next++
	t.byIP[ip] = name
	t.seen[ip] = now
	return ip
}

// coldest returns the fake untouched longest, or 0 when none is past reuseFloor.
// Caller holds the lock.
func (t *faketab) coldest(now time.Time) uint32 {
	var pick uint32
	oldest := now.Add(-reuseFloor)
	for ip := range t.byIP {
		if at, ok := t.seen[ip]; ok && at.Before(oldest) {
			oldest, pick = at, ip
		}
	}
	return pick
}

// lookup resolves a fake back to its name — called on every connect to it, so
// it is also what marks the entry as still in use.
func (t *faketab) lookup(ip uint32) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n, ok := t.byIP[ip]
	if ok {
		t.seen[ip] = time.Now()
	}
	return n, ok
}

// upstreamDNS is the child's ORIGINAL nameservers, kept in the two forms this
// package needs: a resolver for the A path — which builds its own answer — and
// the raw server list for the relay path, which forwards a query verbatim and
// so cannot go through net.Resolver at all.
//
// Dialling them directly bypasses the resolver we just repointed at ourselves,
// so our own lookups don't loop.
//
// With none captured it falls back to a public resolver, which keeps dotted
// names working but sends them somewhere the user did not choose — the caller
// warns when that happens. All three platforms capture the system servers now,
// so this is the last resort it was meant to be rather than the Windows norm.
// The address list is MUTABLE and read under a lock, because the machine's
// nameservers are not a startup fact: a VPN coming up or going down replaces
// them mid-session. Captured once, plug kept forwarding to a resolver that had
// gone away (internal names dead) or missed the one that just appeared
// (internal names never resolving at all) until the session was restarted.
type upstreamDNS struct {
	mu      sync.RWMutex
	addrs   []string      // "host:port", port defaulted on the way in
	timeout time.Duration // how long relay waits; tests shorten it
	// scoped are the resolvers that answer ONLY for a domain and its
	// subdomains: what a corporate VPN pushes for its internal names while the
	// rest of the world keeps going to the machine's ordinary servers. macOS
	// calls them SupplementalMatchDomains, Windows the NRPT, systemd-resolved
	// per-link routing domains. plug used to take the primary servers alone and
	// route EVERYTHING through them, so the internal names a VPN exists to serve
	// were the first thing a session broke. Matched longest suffix first.
	scoped []scopedUpstream
}

// scopedUpstream is one such scope: the domain, lower-case, without a leading
// dot, and the servers that answer for it, already dialable.
type scopedUpstream struct {
	domain string
	addrs  []string
}

func newUpstream(servers []string) *upstreamDNS {
	u := &upstreamDNS{
		// Long enough for a slow corporate resolver, short enough to answer before
		// the client's own patience runs out — the A path above uses the same 4s.
		timeout: 4 * time.Second,
	}
	u.set(servers)
	return u
}

// setScoped replaces the domain-scoped resolvers. Domains are normalised here
// so serversFor can compare cheaply, and a scope with no usable server is dropped
// rather than kept as an empty list that would match and then answer nothing.
func (u *upstreamDNS) setScoped(scopes []scopedUpstream) {
	clean := make([]scopedUpstream, 0, len(scopes))
	seen := map[string]bool{}
	for _, sc := range scopes {
		d := strings.ToLower(strings.Trim(strings.TrimSpace(sc.domain), "."))
		addrs := dialable(sc.addrs)
		// OpenVPN Connect lists its domain twice; once is enough.
		if d == "" || len(sc.addrs) == 0 || seen[d] {
			continue
		}
		seen[d] = true
		clean = append(clean, scopedUpstream{domain: d, addrs: addrs})
	}
	u.mu.Lock()
	u.scoped = clean
	addrs := append([]string(nil), u.addrs...)
	u.mu.Unlock()
	// Published beside the ordinary servers, so `plug doctor` says where a
	// VPN's names go and not only where everything else does. One line per
	// scope, "domain=servers", which CurrentUpstreams passes through untouched.
	publishUpstreams(withScopes(addrs, clean))
}

// withScopes renders the published form: the ordinary servers first, then one
// "domain=a,b" line per scope. Pure, for the test.
func withScopes(addrs []string, scopes []scopedUpstream) []string {
	out := append([]string(nil), addrs...)
	for _, sc := range scopes {
		out = append(out, sc.domain+"="+strings.Join(sc.addrs, ","))
	}
	return out
}

// serversFor picks the servers a name goes to: the scoped resolver whose domain
// is the LONGEST suffix of the name, or the ordinary servers when none matches.
//
// Longest wins because scopes nest - corp.example inside example - and the more
// specific one is the one that knows the name. Pure, so the routing decision
// that decides whether a VPN's internal names resolve at all is proven rather
// than reasoned about.
func (u *upstreamDNS) serversFor(name string) []string {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return pickScoped(name, u.scoped, u.addrs)
}

// pickScoped is serversFor without the lock, for the tests.
func pickScoped(name string, scoped []scopedUpstream, def []string) []string {
	n := strings.ToLower(strings.TrimSuffix(name, "."))
	best, bestLen := def, -1
	for _, sc := range scoped {
		if (n == sc.domain || strings.HasSuffix(n, "."+sc.domain)) && len(sc.domain) > bestLen {
			best, bestLen = sc.addrs, len(sc.domain)
		}
	}
	return best
}

// set replaces the servers plug forwards to. Empty falls back to a public
// resolver — which keeps dotted names working but sends them somewhere the user
// did not choose, so the caller announces it.
func (u *upstreamDNS) set(servers []string) {
	addrs := dialable(servers)
	u.mu.Lock()
	u.addrs = addrs
	u.mu.Unlock()
	publishUpstreams(addrs) // so `plug doctor` can say where lookups actually go
}

// dialable turns captured servers into addresses that can be dialled, applying
// the public fallback when there are none.
//
// Separate from set because same() needs the exact same normalisation to compare
// honestly, and set has a side effect: it publishes. Comparing THROUGH set meant
// every silent tick of the watcher rewrote the file `plug doctor` reads, with
// candidates that had not been adopted.
func dialable(servers []string) []string {
	if len(servers) == 0 {
		servers = []string{"8.8.8.8"}
	}
	addrs := make([]string, 0, len(servers))
	for _, s := range servers {
		// The captured servers are bare addresses; SplitHostPort failing is how we
		// tell one from an address that already carries a port.
		if _, _, err := net.SplitHostPort(s); err != nil {
			s = net.JoinHostPort(s, "53")
		}
		addrs = append(addrs, s)
	}
	return addrs
}

// primary is the server every lookup goes to right now.
func (u *upstreamDNS) primary() string {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.addrs[0]
}

// all is every server, in order. A VPN typically pushes two or three, and the
// OS moves to the next when one stops answering — so must the relay, or a
// single sick resolver takes every non-address lookup down with it.
func (u *upstreamDNS) all() []string {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return append([]string(nil), u.addrs...)
}

// same reports whether servers would change nothing — so a refresh that found
// the usual answer stays silent instead of logging every tick.
func (u *upstreamDNS) same(servers []string) bool {
	probe := dialable(servers)
	u.mu.RLock()
	defer u.mu.RUnlock()
	if len(probe) != len(u.addrs) {
		return false
	}
	for i := range probe {
		if probe[i] != u.addrs[i] {
			return false
		}
	}
	return true
}

// relay forwards a query verbatim to the upstream and returns its reply
// verbatim. Verbatim is the whole point: SRV, MX, PTR, TXT, NS, CAA and
// everything else stay whatever the upstream said, with no record type this
// package has to learn how to parse or rebuild.
//
// Returns nil if the upstream said nothing in time — the caller turns that into
// SERVFAIL rather than an invented empty answer.
func (u *upstreamDNS) relay(name string, q []byte) []byte {
	// Every server in turn, not just the first. A VPN pushes several precisely
	// so that one being unreachable is survivable; asking only the primary threw
	// that away and made a single sick resolver look like "no such record".
	// The budget is per server, since a dead one costs its whole timeout.
	//
	// And the RIGHT servers for this name: a scope's own resolver when one
	// claims it, the ordinary ones otherwise. Asking the ordinary servers about
	// a VPN's internal name gets a confident NXDOMAIN from a resolver that has
	// never heard of it, and with the race above that answer wins.
	return raceUpstreams(u.serversFor(name), upstreamStagger, func(addr string) []byte { return u.ask(addr, q) })
}

// upstreamStagger is how long the first server gets alone before the next one is
// asked as well. Short enough that a dead resolver costs a fifth of a second
// rather than its whole timeout, long enough that a healthy one answers first
// and the others are never disturbed.
const upstreamStagger = 200 * time.Millisecond

// raceUpstreams asks the servers in order, but does not wait for one to fail
// before starting the next.
//
// Asking them strictly in turn was correct and slow: the budget is four seconds
// per server, so a single unreachable resolver, which is what a VPN transition
// leaves behind, made EVERY dotted lookup take four seconds, and two of them
// eight. Client libraries give up well before that, so a machine whose primary
// resolver had gone quiet looked like a cluster that did not answer.
//
// Staggered rather than all at once: the healthy case asks one server and stops,
// so the extra queries only exist when the first one is slow. First usable reply
// wins and the rest are abandoned, which is what a resolver is expected to do
// when it is given several servers precisely so one may fail.
func raceUpstreams(servers []string, stagger time.Duration, ask func(string) []byte) []byte {
	if len(servers) == 0 {
		return nil
	}
	// Buffered for every server, so a goroutine whose answer nobody wants still
	// finishes and is collected rather than blocking on a send forever.
	replies := make(chan []byte, len(servers))
	next := 0
	launch := func() {
		addr := servers[next]
		next++
		go func() { replies <- ask(addr) }()
	}
	launch()

	t := time.NewTimer(stagger)
	defer t.Stop()
	for answered := 0; answered < len(servers); {
		select {
		case r := <-replies:
			answered++
			if r != nil {
				return r
			}
			// One said no. Bring the next in immediately rather than waiting out
			// the stagger it no longer needs to share.
			if next < len(servers) {
				launch()
				t.Reset(stagger)
			}
		case <-t.C:
			if next < len(servers) {
				launch()
				t.Reset(stagger)
			}
		}
	}
	return nil
}

// ask sends q to one server and returns its reply, or nil.
func (u *upstreamDNS) ask(addr string, q []byte) []byte {
	c, err := net.DialTimeout("udp", addr, u.timeout)
	if err != nil {
		return nil
	}
	defer c.Close()
	deadline := time.Now().Add(u.timeout)
	_ = c.SetDeadline(deadline)
	if _, err := c.Write(q); err != nil {
		return nil
	}
	buf := make([]byte, 4096)
	// The socket is connected, so only the upstream's packets arrive — but an
	// off-path spoof still has to guess the id, and dropping mismatches is one
	// line. Keep reading until the deadline rather than trusting the first.
	for time.Now().Before(deadline) {
		n, err := c.Read(buf)
		if err != nil {
			return nil
		}
		if n >= 12 && buf[0] == q[0] && buf[1] == q[1] {
			return buf[:n]
		}
	}
	return nil
}

// answerDNS parses the first question and builds a minimal response: A for a
// single-label name → a fake IP in this instance's /24 IF the name exists in a
// connected cluster (check; nil skips the check and always mints); A for a
// dotted name → the real address via the saved upstream; AAAA → NODATA (force
// IPv4); localhost → 127.0.0.1.
func answerDNS(q []byte, tab *faketab, upstream *upstreamDNS, check nameChecker) []byte {
	if len(q) < 13 {
		return nil
	}
	name, p := parseName(q, 12)
	if p+4 > len(q) {
		return nil
	}
	qtype := binary.BigEndian.Uint16(q[p:])
	qend := p + 4

	var answerIP net.IP
	rcode := byte(0)
	switch {
	case qtype == 28 && !relayable(name, tab): // AAAA for a name of ours → NODATA (force v4)
	case qtype == 28:
		// AAAA for a name that is NOT ours. Still NODATA, never a real v6 - a v6
		// answer would send the client straight past the tunnel - but NODATA
		// has to be CONSISTENT with what the upstream says of the name, and
		// that cost a whole day to learn. NODATA means "this name exists, it
		// just has no address of that type". getaddrinfo asks A and AAAA
		// together and walks the search list in order, so with the network's
		// `lan` ahead of plug's `plug` a bare `odb` is first asked as `odb.lan`:
		// the A came back NXDOMAIN from the box in a millisecond, the AAAA came
		// back NODATA from here, and a resolver handed "does not exist" and
		// "exists, no address" for the same name waits on the pair instead of
		// moving on to `odb.plug`. Every process joining a cluster service by
		// its bare name died on a connect timeout - on every version of plug
		// back to the first, on any network whose DHCP announces a search
		// domain. So ask the upstream for the A first: NXDOMAIN there is
		// NXDOMAIN here, and the AAAA of a name that exists stays NODATA.
		if upstream != nil {
			probe := append([]byte(nil), q...)
			binary.BigEndian.PutUint16(probe[p:], 1) // same name, type A
			if reply := upstream.relay(name, probe); reply != nil && len(reply) > 3 && reply[3]&0x0f == 3 {
				rcode = 3
			}
		}
	case qtype != 1:
		// Everything that is not an address: SRV, MX, PTR, TXT, NS… On macOS this
		// stub is the resolver for the WHOLE machine, so answering NODATA broke
		// them host-wide for the length of a session — AD clients, MongoDB
		// seedlists, Consul. Relay them to the upstream instead.
		//
		// Only for names we do not own. A single-label name is a cluster service
		// and a .plug name is our own suffix: neither exists upstream, and asking
		// would leak an internal name to a public resolver to be told so. Our
		// reverse zone is ours by definition. Those stay NODATA, as before.
		if !relayable(name, tab) {
			break
		}
		// Guarded, because this function answers the whole machine's resolver on
		// macOS and the parameter is a pointer nothing declares as mandatory. A
		// caller wired without an upstream would have taken the datapath down on
		// the first query for a name it does not own, which is to say on the
		// first query at all. SERVFAIL is the honest answer: there is no upstream
		// to ask.
		if upstream != nil {
			if reply := upstream.relay(name, q); reply != nil {
				return capReply(reply, q, qend)
			}
		}
		rcode = 2 // SERVFAIL: the upstream said nothing — say that, don't invent an empty answer
	case strings.EqualFold(name, "localhost") || strings.HasSuffix(strings.ToLower(name), ".localhost"):
		answerIP = net.IPv4(127, 0, 0, 1)
	case strings.HasSuffix(strings.ToLower(name), "."+searchSuffix):
		// Windows appended plug0's search suffix (my-service → my-service.plug) to
		// force a DNS query. Strip it and mint the SAME fake IP as the bare name, so
		// the connect maps back to "my-service" for the agent to resolve.
		if base := name[:len(name)-len(searchSuffix)-1]; base != "" && !strings.Contains(base, ".") {
			if check != nil && !check(base) {
				rcode = 3 // honest NXDOMAIN — the name is in no connected cluster
			} else if ip := tab.mint(base); ip != 0 {
				answerIP = net.IPv4(byte(ip>>24), byte(ip>>16), byte(ip>>8), byte(ip))
			} else {
				rcode = 3
			}
		} else {
			rcode = 3
		}
	case !strings.Contains(name, "."): // single-label cluster name → fake
		if check != nil && !check(name) {
			rcode = 3 // honest NXDOMAIN — the name is in no connected cluster
		} else if ip := tab.mint(name); ip != 0 {
			answerIP = net.IPv4(byte(ip>>24), byte(ip>>16), byte(ip>>8), byte(ip))
		} else {
			rcode = 3 // NXDOMAIN — this instance's /24 is exhausted
		}
	case isClusterLongName(name):
		// A Service under its Kubernetes long name: ours, like the bare name,
		// and handled the same way, only the name stays whole so the connect
		// carries the namespace to the agent (see clusterLongName).
		long, _ := clusterLongName(name)
		if check != nil && !check(long) {
			rcode = 3
		} else if ip := tab.mint(long); ip != 0 {
			answerIP = net.IPv4(byte(ip>>24), byte(ip>>16), byte(ip>>8), byte(ip))
		} else {
			rcode = 3
		}
	default: // dotted → resolve for real via the saved upstream
		if upstream == nil {
			// The second of the two places this pointer is followed. Guarded for
			// the same reason: nothing declares it mandatory, and this function
			// answers every query the machine's resolver forwards on macOS.
			rcode = 2 // SERVFAIL: no upstream to resolve it with
			break
		}
		// Relayed as-is, like every other type, and NOT through a Go resolver
		// dialling u.primary(): that resolver went to the ordinary servers for
		// every name, scoped or not, so the 2.15.2 routing of a VPN's names to
		// the VPN's resolver held for AAAA and never for A - the one record
		// that matters. The selftest's scoped probe found it: the scope's
		// resolver was asked for the AAAA, the A went to the box, the name did
		// not resolve. relay picks the servers per name; the reply comes back
		// untouched, CNAME chain and all, which is what the client asked for.
		if reply := upstream.relay(name, q); reply != nil {
			return capReply(reply, q, qend)
		}
		rcode = 2 // SERVFAIL: the upstream said nothing, say that rather than invent NXDOMAIN
	}

	r := make([]byte, 0, 64)
	r = append(r, q[0], q[1]) // id
	r = append(r, 0x81)       // QR=1, RD
	if answerIP != nil {
		r = append(r, 0x80) // RA, RCODE 0
	} else {
		r = append(r, 0x80|rcode)
	}
	r = append(r, 0, 1) // QDCOUNT
	if answerIP != nil {
		r = append(r, 0, 1)
	} else {
		r = append(r, 0, 0)
	}
	switch {
	case answerIP != nil:
		r = append(r, 0, 0, 0, 0) // NS/AR counts
	case rcode == 2:
		r = append(r, 0, 0, 0, 0) // SERVFAIL asserts nothing, so it carries no SOA
	default:
		r = append(r, 0, 1, 0, 0) // one AUTHORITY record: the negative-TTL SOA
	}
	r = append(r, q[12:qend]...) // question
	switch {
	case answerIP != nil:
		r = append(r, 0xC0, 0x0C) // name ptr
		r = append(r, 0, 1, 0, 1) // A, IN
		// TTL 5s, down from 30: this is how long the OS resolver (and, on a
		// plugged workstation, Docker Desktop's VM behind it) may repeat this
		// answer without asking again. Thirty seconds was the floor of a real
		// incident — a session killed and relaunched left a cluster gateway
		// dialling the previous address for the full OS-cache window, whatever
		// plug's own caches did. Five matches the negative SOA MINIMUM below:
		// plug's answers, positive or negative, are honest within five seconds.
		r = append(r, 0, 0, 0, 5)
		r = append(r, 0, 4)
		r = append(r, answerIP.To4()...)
	case rcode == 2: // SERVFAIL — header and question only
	default:
		// Negative answers carry a synthetic SOA whose MINIMUM bounds the
		// client's NEGATIVE cache (RFC 2308). Without it the client picks its
		// own duration — and macOS's mDNSResponder held one NXDOMAIN long
		// enough to outlive the few seconds a -s name is gone while an agent
		// restart re-provisions it: the name was back, every lookup on the
		// machine still failed instantly from the cache. 5s keeps that window
		// honest: absent stays absent, but never longer than it really was.
		r = append(r, 0)          // owner: root
		r = append(r, 0, 6, 0, 1) // SOA, IN
		r = append(r, 0, 0, 0, 5) // TTL
		r = append(r, 0, 22)      // RDLENGTH: mname(1) rname(1) + 5×uint32
		r = append(r, 0)          // MNAME: root
		r = append(r, 0)          // RNAME: root
		r = append(r, 0, 0, 0, 1) // SERIAL
		r = append(r, 0, 0, 0, 5) // REFRESH
		r = append(r, 0, 0, 0, 5) // RETRY
		r = append(r, 0, 0, 0, 5) // EXPIRE
		r = append(r, 0, 0, 0, 5) // MINIMUM — the negative TTL
	}
	return r
}

// isClusterLongName is clusterLongName as a predicate, for the switch above.
func isClusterLongName(name string) bool {
	_, ok := clusterLongName(name)
	return ok
}

// relayable reports whether a name belongs to somebody else, and so may be asked
// upstream. Ours are the ones that only exist here: a single-label cluster
// service, the same Service under its Kubernetes long name, the .plug suffix
// Windows appends to one, and the reverse zone of this instance's own /24.
// Sending any of them out would leak an internal name to a resolver the user
// may not control, to be told what we already know. For the long name it also
// keeps the AAAA a NODATA next to the A we mint: the pair getaddrinfo waits on
// has to agree, the lesson the AAAA case above cost a day to learn.
func relayable(name string, tab *faketab) bool {
	n := strings.ToLower(strings.TrimSuffix(name, "."))
	if !strings.Contains(n, ".") || n == searchSuffix || strings.HasSuffix(n, "."+searchSuffix) || isClusterLongName(n) {
		return false
	}
	// 198.18.<N>.<h> reverses to <h>.<N>.18.198.in-addr.arpa
	base := tab.base
	return !strings.HasSuffix(n, fmt.Sprintf(".%d.%d.%d.in-addr.arpa",
		(base>>8)&0xff, (base>>16)&0xff, (base>>24)&0xff))
}

// maxRelayReply is the largest reply we hand back over the TUN. 1232 is the EDNS0
// payload size the DNS flag day settled on — it clears the 1500-byte MTU with
// room for headers and never fragments.
const maxRelayReply = 1232

// capReply passes the upstream's reply through untouched when it fits, and
// otherwise replaces it with an empty TRUNCATED answer. A datagram over the MTU
// would be fragmented or dropped, and a silently clipped one is a malformed
// message; TC=1 is the protocol's own way to say "too big", which is at least a
// thing the client knows how to read.
func capReply(reply, q []byte, qend int) []byte {
	if len(reply) <= maxRelayReply {
		return reply
	}
	r := make([]byte, 0, qend+2)
	r = append(r, q[0], q[1])             // id
	r = append(r, 0x83)                   // QR=1, TC=1, RD=1
	r = append(r, 0x80)                   // RA, RCODE 0
	r = append(r, 0, 1, 0, 0, 0, 0, 0, 0) // QD=1, no records
	return append(r, q[12:qend]...)       // question
}

func parseName(q []byte, off int) (string, int) {
	var sb strings.Builder
	p := off
	for p < len(q) && q[p] != 0 {
		l := int(q[p])
		p++
		if p+l > len(q) {
			break
		}
		if sb.Len() > 0 {
			sb.WriteByte('.')
		}
		sb.Write(q[p : p+l])
		p += l
	}
	return sb.String(), p + 1 // skip the root 0
}

// FirstInstanceResolver is what a container joining a plug datapath must be
// pointed at: the resolver of instance 0, and the search domain that makes a
// BARE name reach it. Exported for `plug --dockerrun`, which writes a resolv.conf
// for a container that has no other way to learn either.
//
// Instance 0 because a sidecar holding one cluster is the first datapath in its
// own network namespace, always. Composed from the same instanceNet the datapath
// uses rather than spelled out again, so the two cannot drift.
func FirstInstanceResolver() (nameserver, search string) {
	_, dnsIP, _ := instanceNet(0)
	return dnsIP, searchSuffix
}
