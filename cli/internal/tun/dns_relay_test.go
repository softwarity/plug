package tun

import (
	"net"
	"testing"
	"time"
)

// fakeUpstream is a DNS server that answers whatever the test tells it to. It
// records the queries it was asked, which is how the leak tests below prove a
// name never left the machine.
type fakeUpstream struct {
	conn  *net.UDPConn
	reply func(q []byte) []byte // nil → stay silent
	asked chan []byte
}

func newFakeUpstream(t *testing.T, reply func(q []byte) []byte) *fakeUpstream {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	u := &fakeUpstream{conn: c, reply: reply, asked: make(chan []byte, 8)}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, from, err := c.ReadFromUDP(buf)
			if err != nil {
				return
			}
			q := append([]byte(nil), buf[:n]...)
			select {
			case u.asked <- q:
			default:
			}
			if u.reply != nil {
				_, _ = c.WriteToUDP(u.reply(q), from)
			}
		}
	}()
	t.Cleanup(func() { c.Close() })
	return u
}

func (u *fakeUpstream) dns() *upstreamDNS {
	d := newUpstream([]string{u.conn.LocalAddr().String()})
	d.timeout = 300 * time.Millisecond // the real 4s would add 8s to the suite
	return d
}

// wasAsked reports whether anything reached the upstream within a short grace
// period. Absence is the assertion in the leak tests, so it has to wait long
// enough that a query in flight would have landed.
func (u *fakeUpstream) wasAsked() bool {
	select {
	case <-u.asked:
		return true
	case <-time.After(300 * time.Millisecond):
		return false
	}
}

// A resolver that answers NODATA to everything but an address is a resolver that
// breaks SRV, MX and PTR for the whole machine — and on macOS this stub IS the
// whole machine's resolver for the length of a session. The upstream's answer
// must come back untouched, whatever record type it holds.
func TestNonAQueriesAreRelayedVerbatim(t *testing.T) {
	want := []byte{0x12, 0x34, 0x81, 0x80, 0, 1, 0, 1, 0, 0, 0, 0, 'S', 'R', 'V', 'x'}
	up := newFakeUpstream(t, func(q []byte) []byte { return want })

	got := answerDNS(query("_mongodb._tcp.example.com", 33), newFaketab(fakeBase), up.dns(), nil)
	if string(got) != string(want) {
		t.Fatalf("relayed reply = %v, want it byte-for-byte: %v", got, want)
	}
}

// Our own names exist nowhere else, so asking about them upstream tells a
// resolver the user may not control what runs inside their cluster — and gets
// back what we already knew.
func TestOurOwnNamesAreNeverRelayed(t *testing.T) {
	tab := newFaketab(fakeBase) // 198.18.0.0/24
	cases := []struct {
		what  string
		name  string
		qtype uint16
	}{
		{"a cluster service", "mongodb", 33},                     // SRV
		{"the .plug suffix Windows appends", "mongodb.plug", 33}, // SRV
		{"our own reverse zone", "2.0.18.198.in-addr.arpa", 12},  // PTR
		{"the suffix on its own", "plug", 16},                    // TXT
	}
	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			up := newFakeUpstream(t, func(q []byte) []byte { return q })
			resp := answerDNS(query(c.name, c.qtype), tab, up.dns(), nil)
			if up.wasAsked() {
				t.Errorf("%q was sent to the upstream — an internal name left the machine", c.name)
			}
			if len(resp) < 4 {
				t.Fatalf("no response built for %q", c.name)
			}
			if rcode := resp[3] & 0x0F; rcode != 0 {
				t.Errorf("rcode = %d, want 0 (NODATA) for a name we own", rcode)
			}
			if ancount := int(resp[6])<<8 | int(resp[7]); ancount != 0 {
				t.Errorf("ANCOUNT = %d, want 0", ancount)
			}
		})
	}
}

// AAAA stays NODATA on purpose: the fake addresses are v4, and answering with a
// real v6 address would send the client straight out of the tunnel.
func TestAAAAIsStillNotRelayed(t *testing.T) {
	up := newFakeUpstream(t, func(q []byte) []byte { return q })
	resp := answerDNS(query("example.com", 28), newFaketab(fakeBase), up.dns(), nil)
	if up.wasAsked() {
		t.Error("an AAAA query was relayed — v6 would bypass the fake-address path")
	}
	if resp[3]&0x0F != 0 || int(resp[6])<<8|int(resp[7]) != 0 {
		t.Errorf("AAAA should be NODATA, got rcode=%d ancount=%d", resp[3]&0x0F, int(resp[6])<<8|int(resp[7]))
	}
}

// A silent upstream is a failure, not an empty answer. NODATA would assert the
// name has no such record, which we have no way of knowing.
func TestASilentUpstreamIsServfailNotNodata(t *testing.T) {
	up := newFakeUpstream(t, nil) // takes the query, never answers

	start := time.Now()
	resp := answerDNS(query("_ldap._tcp.corp.example.com", 33), newFaketab(fakeBase), up.dns(), nil)
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("a dead upstream held the answer for %v — clients time out first", d)
	}
	if rcode := resp[3] & 0x0F; rcode != 2 {
		t.Errorf("rcode = %d, want 2 (SERVFAIL)", rcode)
	}
	// SERVFAIL asserts nothing, so it must not carry the negative-caching SOA:
	// a client caching "no such record" from a timeout would be wrong for as
	// long as it held it.
	if nscount := int(resp[8])<<8 | int(resp[9]); nscount != 0 {
		t.Errorf("NSCOUNT = %d, want 0 — SERVFAIL must not carry an SOA", nscount)
	}
}

// A reply past the MTU would be fragmented or dropped, and one silently clipped
// would be malformed. TC=1 is the protocol's own way to say "too big".
func TestAnOversizedReplyComesBackTruncated(t *testing.T) {
	big := make([]byte, maxRelayReply+1)
	copy(big, []byte{0x12, 0x34, 0x81, 0x80, 0, 1, 0, 1, 0, 0, 0, 0})
	up := newFakeUpstream(t, func(q []byte) []byte { return big })

	resp := answerDNS(query("huge.example.com", 16), newFaketab(fakeBase), up.dns(), nil)
	if len(resp) > maxRelayReply {
		t.Fatalf("handed back %d bytes, over the %d cap", len(resp), maxRelayReply)
	}
	if resp[2]&0x02 == 0 {
		t.Error("TC flag not set on an oversized reply")
	}
	if ancount := int(resp[6])<<8 | int(resp[7]); ancount != 0 {
		t.Errorf("ANCOUNT = %d, want 0 — a truncated reply carries no records", ancount)
	}
}

// The id is the only thing tying a reply to its question on a connected UDP
// socket; a mismatched one is somebody else's packet.
func TestAMismatchedIdIsNotAccepted(t *testing.T) {
	up := newFakeUpstream(t, func(q []byte) []byte {
		bad := append([]byte(nil), q...)
		bad[0], bad[1] = 0xFF, 0xFF // not the id we asked with
		bad[2], bad[3] = 0x81, 0x80
		return bad
	})

	resp := answerDNS(query("spoof.example.com", 33), newFaketab(fakeBase), up.dns(), nil)
	if rcode := resp[3] & 0x0F; rcode != 2 {
		t.Errorf("rcode = %d, want 2 (SERVFAIL) — a reply with the wrong id must be ignored", rcode)
	}
}

// A VPN pushes two or three resolvers so that one being unreachable is
// survivable. Asking only the first threw that away: a single sick server made
// every non-address lookup look like "no such record", on a machine whose OS
// would have moved to the next one without blinking.
func TestRelayMovesToTheNextServerWhenOneIsSilent(t *testing.T) {
	dead := newFakeUpstream(t, nil) // takes the query, never answers
	want := []byte{0x12, 0x34, 0x81, 0x80, 0, 1, 0, 1, 0, 0, 0, 0, 'S', 'R', 'V'}
	live := newFakeUpstream(t, func(q []byte) []byte { return want })

	u := newUpstream([]string{dead.conn.LocalAddr().String(), live.conn.LocalAddr().String()})
	u.timeout = 300 * time.Millisecond

	got := answerDNS(query("_sip._tcp.example.com", 33), newFaketab(fakeBase), u, nil)
	if string(got) != string(want) {
		t.Fatalf("relay gave %v — it must fall through to the second server", got)
	}
	if !dead.wasAsked() {
		t.Error("the first server was never tried — order must be honoured")
	}
}

// All of them silent is still SERVFAIL, not an invented empty answer.
func TestRelayWithEveryServerSilentIsStillServfail(t *testing.T) {
	a := newFakeUpstream(t, nil)
	b := newFakeUpstream(t, nil)
	u := newUpstream([]string{a.conn.LocalAddr().String(), b.conn.LocalAddr().String()})
	u.timeout = 200 * time.Millisecond

	resp := answerDNS(query("_sip._tcp.example.com", 33), newFaketab(fakeBase), u, nil)
	if rcode := resp[3] & 0x0F; rcode != 2 {
		t.Errorf("rcode = %d, want 2 (SERVFAIL)", rcode)
	}
}
