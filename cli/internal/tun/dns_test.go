package tun

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
)

// query builds a minimal DNS query packet for name/qtype (class IN).
func query(name string, qtype uint16) []byte {
	b := []byte{0x12, 0x34, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0} // header, QDCOUNT=1
	for _, part := range strings.Split(name, ".") {
		b = append(b, byte(len(part)))
		b = append(b, part...)
	}
	b = append(b, 0) // root label
	b = append(b, byte(qtype>>8), byte(qtype), 0, 1)
	return b
}

// mask24 is the /24 network mask — each instance mints within 198.18.<N>.0/24.
const mask24 = 0xFFFFFF00

func TestFaketabMintStableAndInRange(t *testing.T) {
	tab := newFaketab(fakeBase) // instance 0: 198.18.0.0/24
	a := tab.mint("grpc")
	b := tab.mint("grpc")
	if a != b {
		t.Fatalf("mint not idempotent: %08x vs %08x", a, b)
	}
	if a&mask24 != fakeBase {
		t.Fatalf("minted %s is not in the instance /24", ipStr(a))
	}
	if a == tab.dnsIP() {
		t.Fatalf("minted the reserved DNS IP %s", ipStr(a))
	}
	if tab.mint("postgres") == a {
		t.Fatal("distinct names must get distinct fakes")
	}
	if name, ok := tab.lookup(a); !ok || name != "grpc" {
		t.Fatalf("lookup(%s) = %q,%v; want grpc,true", ipStr(a), name, ok)
	}
	if _, ok := tab.lookup(0xDEADBEEF); ok {
		t.Fatal("lookup of an unminted fake must miss")
	}
}

// TestMintReservesDNSAndExhausts checks the DNS host (.53) is never handed out
// and that a full /24 exhausts to 0 (NXDOMAIN) rather than aliasing an IP.
func TestMintReservesDNSAndExhausts(t *testing.T) {
	tab := newFaketab(fakeBase)
	dns := tab.dnsIP()
	n := 0
	for {
		ip := tab.mint(fmt.Sprintf("svc%d", n))
		if ip == 0 {
			break
		}
		if ip == dns {
			t.Fatalf("minted the reserved DNS IP %s", ipStr(ip))
		}
		if n++; n > 300 {
			t.Fatal("mint never exhausted the /24")
		}
	}
	// hosts 1..254 minus the reserved .53 = 253 usable service IPs.
	if n != 253 {
		t.Fatalf("expected 253 mintable service IPs, got %d", n)
	}
}

func TestParseName(t *testing.T) {
	q := query("api.internal", 1)
	name, off := parseName(q, 12)
	if name != "api.internal" {
		t.Fatalf("parseName = %q; want api.internal", name)
	}
	if off != 12+len("\x03api\x08internal\x00") {
		t.Fatalf("parseName offset = %d; unexpected", off)
	}
}

func answerIPv4(resp []byte) (uint32, bool) {
	if len(resp) < 4 || binary.BigEndian.Uint16(resp[6:]) == 0 {
		return 0, false // ANCOUNT == 0
	}
	return binary.BigEndian.Uint32(resp[len(resp)-4:]), true
}

func TestAnswerSingleLabelMintsFake(t *testing.T) {
	tab := newFaketab(fakeBase)
	resp := answerDNS(query("grpc", 1), tab, newUpstream(nil), nil)
	ip, ok := answerIPv4(resp)
	if !ok {
		t.Fatal("expected an A answer for a single-label name")
	}
	if ip&mask24 != fakeBase {
		t.Fatalf("answer %s is not a fake in the instance /24", ipStr(ip))
	}
	if name, ok := tab.lookup(ip); !ok || name != "grpc" {
		t.Fatalf("fake %s not mapped back to grpc (got %q,%v)", ipStr(ip), name, ok)
	}
}

func TestAnswerLocalhostIsLoopback(t *testing.T) {
	resp := answerDNS(query("localhost", 1), newFaketab(fakeBase), newUpstream(nil), nil)
	ip, ok := answerIPv4(resp)
	if !ok || ip != 0x7F000001 {
		t.Fatalf("localhost → %08x,%v; want 127.0.0.1", ip, ok)
	}
}

func TestAnswerAAAAIsNodata(t *testing.T) {
	resp := answerDNS(query("grpc", 28), newFaketab(fakeBase), newUpstream(nil), nil)
	if _, ok := answerIPv4(resp); ok {
		t.Fatal("AAAA must be NODATA (no answer) to force IPv4")
	}
}

// The Windows search-suffix path must land on the SAME fake as the bare name:
// getaddrinfo asks for "svc.plug" (suffix appended so a real DNS query fires),
// answerDNS strips it, and the connect must map back to "svc" for the agent.
func TestAnswerDNSStripsSearchSuffix(t *testing.T) {
	tab := newFaketab(fakeBase)
	bare := answerDNS(query("svc", 1), tab, newUpstream(nil), nil)
	suffixed := answerDNS(query("svc."+searchSuffix, 1), tab, newUpstream(nil), nil)
	if bare == nil || suffixed == nil {
		t.Fatal("nil response")
	}
	if !bytes.Equal(bare[len(bare)-4:], suffixed[len(suffixed)-4:]) {
		t.Fatalf("suffixed name minted a DIFFERENT fake: %v vs %v",
			bare[len(bare)-4:], suffixed[len(suffixed)-4:])
	}
	if name, ok := tab.lookup(fakeBase | 1); !ok || name != "svc" {
		t.Fatalf("faketab must map back to the BARE name, got %q,%v", name, ok)
	}
}

// Negative answers must carry a SOA bounding the client's negative cache (RFC
// 2308): without one, macOS's mDNSResponder held an NXDOMAIN from the few
// seconds a -s name was gone (agent restart, signpost re-provisioned) long
// after the name was back — every lookup on the machine failing instantly
// without ever reaching the stub again.
func TestNegativeAnswersCarryShortSOA(t *testing.T) {
	tab := newFaketab(fakeBase)
	deny := func(string) bool { return false } // every name: not in the cluster
	for _, c := range []struct {
		name  string
		resp  []byte
		rcode byte
	}{
		{"NXDOMAIN (honest check)", answerDNS(query("gone", 1), tab, newUpstream(nil), deny), 3},
		{"NODATA (AAAA)", answerDNS(query("grpc", 28), tab, newUpstream(nil), nil), 0},
	} {
		r := c.resp
		if r == nil {
			t.Fatalf("%s: nil response", c.name)
		}
		if got := r[3] & 0x0F; got != c.rcode {
			t.Errorf("%s: rcode = %d, want %d", c.name, got, c.rcode)
		}
		if an := int(r[6])<<8 | int(r[7]); an != 0 {
			t.Errorf("%s: ANCOUNT = %d, want 0", c.name, an)
		}
		if ns := int(r[8])<<8 | int(r[9]); ns != 1 {
			t.Fatalf("%s: NSCOUNT = %d, want 1 (the SOA)", c.name, ns)
		}
		// The authority record sits right after the question.
		_, p := parseName(r, 12)
		p += 4         // qtype+qclass
		if r[p] != 0 { // owner: root
			t.Fatalf("%s: SOA owner not root", c.name)
		}
		p++
		if typ := int(r[p])<<8 | int(r[p+1]); typ != 6 {
			t.Fatalf("%s: authority type = %d, want SOA(6)", c.name, typ)
		}
		ttl := uint32(r[p+4])<<24 | uint32(r[p+5])<<16 | uint32(r[p+6])<<8 | uint32(r[p+7])
		min := r[len(r)-4:]
		minimum := uint32(min[0])<<24 | uint32(min[1])<<16 | uint32(min[2])<<8 | uint32(min[3])
		if ttl > 5 || minimum > 5 {
			t.Errorf("%s: negative TTL %d/MINIMUM %d — must stay ≤5s", c.name, ttl, minimum)
		}
	}
}
