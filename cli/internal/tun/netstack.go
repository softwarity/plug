package tun

import (
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

// buildStack wires a userspace TCP/IP stack onto a channel endpoint. The NIC is
// promiscuous + spoofing with a catch-all v4 route, so the stack accepts SYNs to
// ANY fake destination and may reply from it. Every accepted TCP flow is handed
// to handleTCP, which splices it to the SSH tunnel by name; DNS (UDP:53 to the
// instance's dnsIP) is answered in-stack by handleDNS.
// dialFunc picks the cluster transport for an intercepted flow by its source
// port, returning the transport and the cluster key it was attributed to (for
// logs), or ok=false to refuse it (unattributable → RST). Single-cluster returns
// a constant (constDial); multicluster resolves srcPort→PID→cluster→tunnel.
type dialFunc func(srcPort uint16) (d Dialer, cluster string, ok bool)

// constDial is the single-cluster case: every flow goes to the one transport, no
// attribution. This keeps the proven single-cluster datapath behaviour exact.
func constDial(tr Dialer) dialFunc {
	return func(uint16) (Dialer, string, bool) { return tr, "", true }
}

// refusedLimiter throttles the "unattributable flow" log. While a cluster is
// active the TUN also catches the machine's own bare-name probes (Windows WPAD,
// mDNS…) that can't be attributed to any cluster — dozens of lines a second that
// otherwise drown the real datapath log. First sight of a name logs; repeats are
// dropped for the window.
var refusedLimiter = newLogLimiter(30 * time.Second)

// connLimiter throttles the per-connection "→ target (by name)" line: the FIRST
// flow to a target is worth seeing (proof the datapath works for that name), but
// a chatty app opens hundreds — they drowned the child's own console output.
var connLimiter = newLogLimiter(10 * time.Minute)

// dialErrLimiter throttles per-target dial failures: a service that is down
// while an app retries in a loop used to write one line per attempt (a real
// 300MB daemon.log). First failure logs; repeats stay quiet for the window.
var dialErrLimiter = newLogLimiter(30 * time.Second)

// udpDropLimiter throttles the "udp dropped" line: plug carries TCP only (SSH
// direct-tcpip is stream-only), so a named UDP flow goes nowhere — but the old
// silent drop left the app hanging with no diagnostic at all. First sight of a
// target names the drop; a chatty client stays quiet for the window.
var udpDropLimiter = newLogLimiter(30 * time.Second)

type logLimiter struct {
	mu     sync.Mutex
	last   map[string]time.Time
	window time.Duration
}

func newLogLimiter(window time.Duration) *logLimiter {
	return &logLimiter{last: map[string]time.Time{}, window: window}
}

// allow reports whether key may be logged now, recording the time when it may.
func (l *logLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if t, ok := l.last[key]; ok && now.Sub(t) < l.window {
		return false
	}
	l.last[key] = now
	return true
}

func buildStack(tab *faketab, df dialFunc, upstream *upstreamDNS, check nameChecker, log logfn) (*stack.Stack, *channel.Endpoint) {
	s := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	ep := channel.New(512, mtu, "")
	s.CreateNIC(nicID, ep)
	s.SetPromiscuousMode(nicID, true)
	s.SetSpoofing(nicID, true)
	// All fakes are IPv4 (the resolver hands out 198.18/15 and NODATAs AAAA), so a
	// single v4 catch-all route suffices; stray v6 packets are simply dropped.
	s.AddRoute(tcpip.Route{Destination: header.IPv4EmptySubnet, NIC: nicID})

	tcpFwd := tcp.NewForwarder(s, 0, 2048, func(r *tcp.ForwarderRequest) {
		handleTCP(r, tab, df, log)
	})
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)

	// DNS lives INSIDE the stack: the child (and, on macOS, the whole system
	// resolver we repoint) sends queries to dnsIP:53; they arrive as IP packets
	// and this UDP forwarder answers them. No loopback socket — so getaddrinfo on
	// macOS, which ignores /etc/resolv.conf, reaches us via the system resolver.
	udpFwd := udp.NewForwarder(s, func(r *udp.ForwarderRequest) {
		handleDNS(r, tab, upstream, check, log)
	})
	s.SetTransportProtocolHandler(udp.ProtocolNumber, udpFwd.HandlePacket)
	return s, ep
}

// handleTCP completes the netstack handshake for one intercepted flow, maps its
// fake destination back to the cluster name, dials the tunnel, and relays.
func handleTCP(r *tcp.ForwarderRequest, tab *faketab, df dialFunc, log logfn) {
	id := r.ID()
	fake := addrToU32(id.LocalAddress) // LocalAddress/Port = the original destination
	name, ok := tab.lookup(fake)
	if !ok {
		r.Complete(true) // RST — we never minted this fake
		return
	}
	// Pick the cluster this flow belongs to. RemotePort is the app's source port,
	// the key the multicluster router walks (srcPort→PID→ancestry→cluster). We
	// refuse an unattributable flow rather than mis-route it. Single-cluster
	// (constDial) always attributes, so this path is unchanged there.
	dialer, cluster, ok := df(id.RemotePort)
	if !ok {
		if refusedLimiter.allow(name) {
			log.f("tun: → %s: refused (unattributable flow; repeats hidden)", name)
		}
		r.Complete(true) // RST — flow can't be attributed to a cluster
		return
	}
	via := ""
	if cluster != "" {
		via = " via " + cluster
	}
	target := net.JoinHostPort(name, strconv.Itoa(int(id.LocalPort)))

	// Dial the cluster BEFORE accepting the client's connection, off the forwarder
	// goroutine. A failed dial then RSTs at once (Complete(true)) instead of
	// half-opening a connection the client keeps retransmitting into — which
	// spammed the log and raced the SSH channel. It also makes reachability honest:
	// the client sees ESTABLISHED only once the flow is spliced end to end, so a
	// name that isn't in this cluster is cleanly refused.
	go func() {
		up, err := dialer.DialCluster(target)
		if err != nil {
			if dialErrLimiter.allow(target) {
				log.f("tun: %s%s: %v (repeats hidden 30s)", target, via, err)
			}
			r.Complete(true) // RST — unreachable in that cluster
			return
		}
		var wq waiter.Queue
		ep, terr := r.CreateEndpoint(&wq)
		if terr != nil {
			up.Close()
			r.Complete(true)
			return
		}
		r.Complete(false) // accept — established end to end
		if connLimiter.allow(target) {
			log.f("tun: %s:%d → %s%s (by name; more flows to it stay quiet)", ipStr(fake), id.LocalPort, target, via)
		}
		relay(gonet.NewTCPConn(&wq, ep), up)
	}()
}

// handleDNS answers one DNS query that arrived at this instance's dnsIP:53
// through the TUN. gVisor's UDP forwarder binds the endpoint to the packet's
// destination (dnsIP), connects it to the source, and re-injects the query — so
// Read yields the question and Write replies to the client. Any UDP flow that is
// NOT our resolver is drained and dropped (CreateEndpoint+Close, so the cloned
// packet is released) — LOUDLY when it targeted a minted name: plug serves only
// DNS in-stack, and the app deserves to know why nothing answers.
func handleDNS(r *udp.ForwarderRequest, tab *faketab, upstream *upstreamDNS, check nameChecker, log logfn) {
	var wq waiter.Queue
	ep, terr := r.CreateEndpoint(&wq)
	if terr != nil {
		return
	}
	id := r.ID()
	if fake := addrToU32(id.LocalAddress); fake != tab.dnsIP() || id.LocalPort != 53 {
		if name, ok := tab.lookup(fake); ok && udpDropLimiter.allow(name) {
			log.f("tun: udp %s:%d dropped — plug tunnels TCP only (repeats hidden 30s)", name, id.LocalPort)
		}
		ep.Close()
		return
	}
	conn := gonet.NewUDPConn(&wq, ep)
	go func() {
		defer conn.Close()
		// 1232, not 512: a query carrying an EDNS0 OPT record announcing that size
		// is itself allowed to be that long, and a short read would truncate the
		// question before it was ever parsed.
		buf := make([]byte, maxRelayReply)
		// A resolver (glibc getaddrinfo) sends A and AAAA from the SAME socket, so
		// both land on THIS endpoint. Read in a loop and answer each — reading only
		// once loses the second query, and the client then stalls until its retry
		// timeout (which e.g. blows pymongo's 5s serverSelectionTimeout).
		for {
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			n, err := conn.Read(buf)
			if err != nil {
				return // idle past the deadline (or closed) → done with this client
			}
			if resp := answerDNS(buf[:n], tab, upstream, check); resp != nil {
				_, _ = conn.Write(resp)
			}
		}
	}()
}

// closeWriter is the half-close capability shared by gonet.TCPConn, *net.TCPConn
// and the SSH channel conn — used to propagate EOF in one direction.
type closeWriter interface{ CloseWrite() error }

// relay splices two connections, half-closing each side on EOF and tearing both
// down once both directions finish.
// One of THREE copies of this function, and the only one that differed. The
// others are cli/internal/tunnel/transport.go and agent/main.go; the agent is a
// separate module, so a shared one would mean publishing a package for fifteen
// lines. Kept duplicated on purpose, and now kept IDENTICAL, which is the part
// that was not true.
//
// The difference was the else branch below. Without it, a direction that
// finished copying into a conn with no CloseWrite signalled nothing, so the
// other direction could wait on an EOF that never came and this function never
// returned. It does not bite today - both ends here (gonet.TCPConn, ssh.Channel)
// implement CloseWrite - which is exactly why it survived three copies.
func relay(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		io.Copy(dst, src)
		if cw, ok := dst.(closeWriter); ok {
			cw.CloseWrite()
		} else {
			dst.Close()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
	a.Close()
	b.Close()
}

// addrToU32 packs a gVisor IPv4 address into the fake-table key space.
func addrToU32(a tcpip.Address) uint32 {
	if a.Len() != 4 {
		return 0
	}
	b := a.As4()
	return binary.BigEndian.Uint32(b[:])
}
