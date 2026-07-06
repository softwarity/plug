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
	// 198.18.0.0/15 — the RFC 2544 benchmarking range: nothing routes it for real,
	// and unlike class-E 240/4 the Windows TCP/IP stack actually routes it (Windows
	// rejects 240/4 as a martian at connect(), so the TUN was unreachable there).
	fakeBase = 0xC6120000 // 198.18.0.0
	fakeMask = 0xFFFE0000 // /15
)

// faketab maps minted fake IPs (240/4) to cluster names. The DNS server mints;
// the netstack forwarder looks up. In-process, shared, mutex-guarded.
type faketab struct {
	mu   sync.Mutex
	byIP map[uint32]string
	next uint32
}

func newFaketab() *faketab { return &faketab{byIP: map[uint32]string{}, next: 1} }

func (t *faketab) mint(name string) uint32 {
	t.mu.Lock()
	defer t.mu.Unlock()
	for ip, n := range t.byIP {
		if n == name {
			return ip
		}
	}
	ip := uint32(fakeBase) | (t.next & ^uint32(fakeMask))
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
// nameservers directly (bypassing the resolv.conf we just repointed at
// ourselves), so our dotted-name lookups don't loop. Falls back to a public
// resolver if the child had none.
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

func serveDNS(c *net.UDPConn, tab *faketab, upstream *net.Resolver, log logfn) {
	buf := make([]byte, 512)
	for {
		n, from, err := c.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if resp := answerDNS(buf[:n], tab, upstream); resp != nil {
			c.WriteToUDP(resp, from)
		}
	}
}

// answerDNS parses the first question and builds a minimal response: A for a
// single-label name → a fake IP; A for a dotted name → the real address; AAAA →
// NODATA (force IPv4); localhost → 127.0.0.1.
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
	case !strings.Contains(name, "."): // single-label cluster name → fake
		ip := tab.mint(name)
		answerIP = net.IPv4(byte(ip>>24), byte(ip>>16), byte(ip>>8), byte(ip))
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
