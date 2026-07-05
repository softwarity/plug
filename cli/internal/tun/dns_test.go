package tun

import (
	"encoding/binary"
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

func TestFaketabMintStableAndInRange(t *testing.T) {
	tab := newFaketab()
	a := tab.mint("grpc")
	b := tab.mint("grpc")
	if a != b {
		t.Fatalf("mint not idempotent: %08x vs %08x", a, b)
	}
	if a&fakeMask != fakeBase {
		t.Fatalf("minted %s is not in 240/4", ipStr(a))
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
	tab := newFaketab()
	resp := answerDNS(query("grpc", 1), tab, upstreamResolver(nil))
	ip, ok := answerIPv4(resp)
	if !ok {
		t.Fatal("expected an A answer for a single-label name")
	}
	if ip&fakeMask != fakeBase {
		t.Fatalf("answer %s is not a 240/4 fake", ipStr(ip))
	}
	if name, ok := tab.lookup(ip); !ok || name != "grpc" {
		t.Fatalf("fake %s not mapped back to grpc (got %q,%v)", ipStr(ip), name, ok)
	}
}

func TestAnswerLocalhostIsLoopback(t *testing.T) {
	resp := answerDNS(query("localhost", 1), newFaketab(), upstreamResolver(nil))
	ip, ok := answerIPv4(resp)
	if !ok || ip != 0x7F000001 {
		t.Fatalf("localhost → %08x,%v; want 127.0.0.1", ip, ok)
	}
}

func TestAnswerAAAAIsNodata(t *testing.T) {
	resp := answerDNS(query("grpc", 28), newFaketab(), upstreamResolver(nil))
	if _, ok := answerIPv4(resp); ok {
		t.Fatal("AAAA must be NODATA (no answer) to force IPv4")
	}
}
