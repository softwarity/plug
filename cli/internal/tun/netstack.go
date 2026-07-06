package tun

import (
	"encoding/binary"
	"io"
	"net"
	"strconv"
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
func buildStack(tab *faketab, tr Dialer, upstream *net.Resolver, log logfn) (*stack.Stack, *channel.Endpoint) {
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
		handleTCP(r, tab, tr, log)
	})
	s.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)

	// DNS lives INSIDE the stack: the child (and, on macOS, the whole system
	// resolver we repoint) sends queries to dnsIP:53; they arrive as IP packets
	// and this UDP forwarder answers them. No loopback socket — so getaddrinfo on
	// macOS, which ignores /etc/resolv.conf, reaches us via the system resolver.
	udpFwd := udp.NewForwarder(s, func(r *udp.ForwarderRequest) {
		handleDNS(r, tab, upstream, log)
	})
	s.SetTransportProtocolHandler(udp.ProtocolNumber, udpFwd.HandlePacket)
	return s, ep
}

// handleTCP completes the netstack handshake for one intercepted flow, maps its
// fake destination back to the cluster name, dials the tunnel, and relays.
func handleTCP(r *tcp.ForwarderRequest, tab *faketab, tr Dialer, log logfn) {
	id := r.ID()
	fake := addrToU32(id.LocalAddress) // LocalAddress/Port = the original destination
	name, ok := tab.lookup(fake)
	if !ok {
		r.Complete(true) // RST — we never minted this fake
		return
	}

	var wq waiter.Queue
	ep, terr := r.CreateEndpoint(&wq)
	if terr != nil {
		r.Complete(true)
		return
	}
	r.Complete(false)
	local := gonet.NewTCPConn(&wq, ep)

	target := net.JoinHostPort(name, strconv.Itoa(int(id.LocalPort)))
	up, err := tr.DialCluster(target)
	if err != nil {
		log.f("tun: cluster %s: %v", target, err)
		local.Close()
		return
	}
	log.f("tun: %s:%d → %s (by name)", ipStr(fake), id.LocalPort, target)
	go relay(local, up)
}

// handleDNS answers one DNS query that arrived at this instance's dnsIP:53
// through the TUN. gVisor's UDP forwarder binds the endpoint to the packet's
// destination (dnsIP), connects it to the source, and re-injects the query — so
// Read yields the question and Write replies to the client. Any UDP flow that is
// NOT our resolver is drained and dropped (CreateEndpoint+Close, so the cloned
// packet is released): plug serves only DNS in-stack.
func handleDNS(r *udp.ForwarderRequest, tab *faketab, upstream *net.Resolver, log logfn) {
	var wq waiter.Queue
	ep, terr := r.CreateEndpoint(&wq)
	if terr != nil {
		return
	}
	id := r.ID()
	if addrToU32(id.LocalAddress) != tab.dnsIP() || id.LocalPort != 53 {
		ep.Close()
		return
	}
	conn := gonet.NewUDPConn(&wq, ep)
	go func() {
		defer conn.Close()
		buf := make([]byte, 512)
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
			if resp := answerDNS(buf[:n], tab, upstream); resp != nil {
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
func relay(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		io.Copy(dst, src)
		if cw, ok := dst.(closeWriter); ok {
			cw.CloseWrite()
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
