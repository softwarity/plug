package tun

import (
	"testing"
	"time"
)

// nxdomainReply builds an NXDOMAIN answer to q, as a real resolver would.
func nxdomainReply(q []byte) []byte {
	r := append([]byte(nil), q...)
	r[2] |= 0x80             // QR
	r[3] = (r[3] & 0xf0) | 3 // RCODE 3
	return r
}

// The case a laptop hit for a whole day. The network's DHCP announces a search
// domain, `lan`, and plug appends its own, `plug`, AFTER it. getaddrinfo tries
// the suffixes in order, so a bare `odb` is asked as `odb.lan` FIRST - relayed
// to the box, which says NXDOMAIN in a millisecond - and only then as
// `odb.plug`, which plug answers itself. Every step of that is fine as long as
// plug answers `odb.lan` promptly. It did not: the first question hung, so
// getaddrinfo never reached the second, and every process joining a cluster
// service by its bare name died on a connect timeout.
//
// So: NXDOMAIN from the upstream must come back as NXDOMAIN, and fast.
func TestAForeignSearchDomainIsAnsweredPromptly(t *testing.T) {
	up := newFakeUpstream(t, nxdomainReply)
	tab := newFaketab(fakeBase)

	s := time.Now()
	resp := answerDNS(query("odb.lan", 1), tab, up.dns(), nil)
	took := time.Since(s)

	if resp == nil {
		t.Fatal("no answer at all for odb.lan: getaddrinfo is left waiting, and never tries odb.plug")
	}
	if rcode := resp[3] & 0x0f; rcode != 3 {
		t.Fatalf("odb.lan answered with rcode %d, want NXDOMAIN (3) as the upstream said", rcode)
	}
	if took > 150*time.Millisecond {
		t.Fatalf("odb.lan took %v to be refused; the upstream answered instantly", took)
	}
	if !up.wasAsked() {
		t.Fatal("the upstream was never asked: plug decided odb.lan on its own")
	}
}

// And the same question for the AAAA record, which getaddrinfo asks in parallel
// with the A: a stall on either one holds the whole lookup.
func TestAForeignSearchDomainIsAnsweredPromptlyForAAAA(t *testing.T) {
	up := newFakeUpstream(t, nxdomainReply)
	s := time.Now()
	resp := answerDNS(query("odb.lan", 28), newFaketab(fakeBase), up.dns(), nil)
	if resp == nil || resp[3]&0x0f != 3 {
		t.Fatalf("odb.lan AAAA: %v", resp)
	}
	if took := time.Since(s); took > 150*time.Millisecond {
		t.Fatalf("odb.lan AAAA took %v", took)
	}
}

// The other half must not move: a cluster name's AAAA stays NODATA, so every
// runtime is forced onto the v4 fake and the tunnel. Relaying it would leak an
// internal name upstream, and a v6 answer would route around plug entirely.
func TestAClusterNamesAAAAStillForcesV4(t *testing.T) {
	up := newFakeUpstream(t, nxdomainReply)
	tab := newFaketab(fakeBase)
	for _, name := range []string{"odb", "odb.plug"} {
		resp := answerDNS(query(name, 28), tab, up.dns(), nil)
		if resp == nil || resp[3]&0x0f != 0 {
			t.Fatalf("%s AAAA: want NODATA (rcode 0, no answer), got %v", name, resp)
		}
		if up.wasAsked() {
			t.Fatalf("%s AAAA was relayed upstream: an internal name leaked", name)
		}
	}
}
