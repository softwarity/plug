package tunnel

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

// Resolver is a split-horizon DNS proxy. It classifies each query by shape and
// routes it to the likely owner, falling back to the other on a negative
// answer:
//
//   - single-label names (no dot: "pdfbox", "mongodb")  → cluster first
//   - dotted FQDNs ("github.com", "x.campbellsci.com")  → host resolvers first
//
// Routing single-label names straight to the cluster also avoids the host
// applying its search domains (e.g. "pdfbox" → "pdfbox.corp.example.com"),
// which would be slow and could mis-resolve.
type Resolver struct {
	tr        *Transport // cluster side (DNS over TCP to 127.0.0.11)
	upstreams []string   // host resolvers, each "ip:53"
	srcPort   int        // fixed source port for host queries (pf exemption); 0 = any
	logf      Logf
}

// NewResolver builds a split resolver. upstreams are the host's real DNS
// servers (host:port); srcPort, if non-zero, is the source port host queries
// are sent from so a firewall rule can exempt them from redirection.
func NewResolver(tr *Transport, upstreams []string, srcPort int, logf Logf) *Resolver {
	return &Resolver{tr: tr, upstreams: upstreams, srcPort: srcPort, logf: logf}
}

// Serve listens for UDP DNS on listenAddr until ctx is done. It returns the
// bound address (useful when listenAddr uses port 0).
func (r *Resolver) Serve(ctx context.Context, listenAddr string) (string, error) {
	pc, err := net.ListenPacket("udp", listenAddr)
	if err != nil {
		return "", err
	}
	go func() {
		<-ctx.Done()
		pc.Close()
	}()
	go func() {
		buf := make([]byte, 4096)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			q := make([]byte, n)
			copy(q, buf[:n])
			go func() {
				if resp := r.resolve(q); resp != nil {
					pc.WriteTo(resp, addr)
				}
			}()
		}
	}()
	return pc.LocalAddr().String(), nil
}

func (r *Resolver) resolve(query []byte) []byte {
	single, ok := singleLabel(query)
	if !ok {
		return nil // malformed
	}
	first, second := r.askHost, r.askCluster
	if single {
		first, second = r.askCluster, r.askHost
	}
	if resp := first(query); positive(resp) {
		return resp
	}
	if resp := second(query); resp != nil {
		return resp
	}
	// Both negative: return the first source's (properly-formed) NXDOMAIN.
	return first(query)
}

// askCluster forwards the query to the cluster resolver over the tunnel
// (DNS over TCP to 127.0.0.11:53).
func (r *Resolver) askCluster(query []byte) []byte {
	c, err := r.tr.DialCluster(clusterResolver)
	if err != nil {
		return nil
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	return dnsOverTCP(c, query)
}

// askHost sends the query to the host's own resolvers over UDP. These packets
// leave from srcPort (if set) so the firewall redirect can skip them.
func (r *Resolver) askHost(query []byte) []byte {
	for _, up := range r.upstreams {
		var laddr *net.UDPAddr
		if r.srcPort != 0 {
			laddr = &net.UDPAddr{Port: r.srcPort}
		}
		raddr, err := net.ResolveUDPAddr("udp", up)
		if err != nil {
			continue
		}
		c, err := net.DialUDP("udp", laddr, raddr)
		if err != nil {
			continue
		}
		c.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := c.Write(query); err != nil {
			c.Close()
			continue
		}
		buf := make([]byte, 4096)
		n, err := c.Read(buf)
		c.Close()
		if err == nil {
			return buf[:n]
		}
	}
	return nil
}

// dnsOverTCP sends one DNS message over a stream (2-byte length prefix) and
// returns the reply.
func dnsOverTCP(c net.Conn, query []byte) []byte {
	var framed [2]byte
	binary.BigEndian.PutUint16(framed[:], uint16(len(query)))
	if _, err := c.Write(append(framed[:], query...)); err != nil {
		return nil
	}
	var ln [2]byte
	if _, err := io.ReadFull(c, ln[:]); err != nil {
		return nil
	}
	resp := make([]byte, binary.BigEndian.Uint16(ln[:]))
	if _, err := io.ReadFull(c, resp); err != nil {
		return nil
	}
	return resp
}

// singleLabel reports whether the query's first question is a single-label
// name (no internal dot). ok is false if the query can't be parsed.
func singleLabel(query []byte) (single, ok bool) {
	var p dnsmessage.Parser
	if _, err := p.Start(query); err != nil {
		return false, false
	}
	q, err := p.Question()
	if err != nil {
		return false, false
	}
	name := strings.TrimSuffix(q.Name.String(), ".")
	return name != "" && !strings.Contains(name, "."), true
}

// positive reports whether resp is a usable answer (NOERROR with ≥1 record).
func positive(resp []byte) bool {
	if resp == nil {
		return false
	}
	var p dnsmessage.Parser
	h, err := p.Start(resp)
	if err != nil || h.RCode != dnsmessage.RCodeSuccess {
		return false
	}
	if err := p.SkipAllQuestions(); err != nil {
		return false
	}
	_, err = p.AnswerHeader()
	return err == nil // at least one answer record present
}
