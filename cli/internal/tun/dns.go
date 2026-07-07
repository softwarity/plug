package tun

import (
	"context"
	"encoding/binary"
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

// faketab maps minted fake IPs to cluster names, all within ONE instance's
// 198.18.<N>.0/24. The DNS forwarder mints; the netstack TCP forwarder looks up.
// In-process, shared, mutex-guarded.
type faketab struct {
	mu   sync.Mutex
	base uint32 // 198.18.<N>.0 — this instance's subnet
	byIP map[uint32]string
	next uint32 // next host byte to hand out (1..254)
}

func newFaketab(base uint32) *faketab {
	return &faketab{base: base, byIP: map[uint32]string{}, next: 1}
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
	for ip, n := range t.byIP {
		if n == name {
			return ip
		}
	}
	if t.next == dnsHost {
		t.next++
	}
	if t.next > 254 {
		return 0 // subnet full — the caller NXDOMAINs rather than alias an IP
	}
	ip := t.base | t.next
	t.next++
	t.byIP[ip] = name
	return ip
}

func (t *faketab) lookup(ip uint32) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	n, ok := t.byIP[ip]
	return n, ok
}

// upstreamResolver returns a resolver that dials the child's ORIGINAL
// nameservers directly (bypassing the resolver we just repointed at ourselves),
// so our dotted-name lookups don't loop. Falls back to a public resolver if the
// child had none.
func upstreamResolver(servers []string) *net.Resolver {
	if len(servers) == 0 {
		servers = []string{"8.8.8.8"}
	}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, net.JoinHostPort(servers[0], "53"))
		},
	}
}

// answerDNS parses the first question and builds a minimal response: A for a
// single-label name → a fake IP in this instance's /24; A for a dotted name →
// the real address via the saved upstream; AAAA → NODATA (force IPv4); localhost
// → 127.0.0.1.
func answerDNS(q []byte, tab *faketab, upstream *net.Resolver) []byte {
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
	case qtype == 28: // AAAA → NODATA (force v4)
	case qtype != 1: // non-A → NODATA
	case strings.EqualFold(name, "localhost") || strings.HasSuffix(strings.ToLower(name), ".localhost"):
		answerIP = net.IPv4(127, 0, 0, 1)
	case strings.HasSuffix(strings.ToLower(name), "."+searchSuffix):
		// Windows appended plug0's search suffix (my-service → my-service.plug) to
		// force a DNS query. Strip it and mint the SAME fake IP as the bare name, so
		// the connect maps back to "my-service" for the agent to resolve.
		if base := name[:len(name)-len(searchSuffix)-1]; base != "" && !strings.Contains(base, ".") {
			if ip := tab.mint(base); ip != 0 {
				answerIP = net.IPv4(byte(ip>>24), byte(ip>>16), byte(ip>>8), byte(ip))
			} else {
				rcode = 3
			}
		} else {
			rcode = 3
		}
	case !strings.Contains(name, "."): // single-label cluster name → fake
		if ip := tab.mint(name); ip != 0 {
			answerIP = net.IPv4(byte(ip>>24), byte(ip>>16), byte(ip>>8), byte(ip))
		} else {
			rcode = 3 // NXDOMAIN — this instance's /24 is exhausted
		}
	default: // dotted → resolve for real via the saved upstream
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		if ips, e := upstream.LookupIP(ctx, "ip4", name); e == nil && len(ips) > 0 {
			answerIP = ips[0].To4()
		} else {
			rcode = 3 // NXDOMAIN
		}
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
	r = append(r, 0, 0, 0, 0)    // NS/AR counts
	r = append(r, q[12:qend]...) // question
	if answerIP != nil {
		r = append(r, 0xC0, 0x0C)  // name ptr
		r = append(r, 0, 1, 0, 1)  // A, IN
		r = append(r, 0, 0, 0, 30) // TTL
		r = append(r, 0, 4)
		r = append(r, answerIP.To4()...)
	}
	return r
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
